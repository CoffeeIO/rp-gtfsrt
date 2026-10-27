package siri

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

// KeepJourney is how long a journey's merged state is kept after its last
// message.
const KeepJourney = 6 * time.Hour

// Subscriber receives SIRI-ET event messages from Dataudveksleren, the
// Danish national access point, which delivers each realtime dataset as an
// Azure Service Bus topic subscription over AMQP. A message carries only the
// stops that changed, so the subscriber merges the messages of each journey
// before passing the journey to Store.
type Subscriber struct {
	URLs       []string // amqps://<namespace>/<topic>/<subscription>
	Credential azcore.TokenCredential
	Store      Store
	Metrics    *metrics.Registry
	Logger     *slog.Logger
	Now        func() time.Time

	mu       sync.Mutex
	journeys map[string]state
	stats    SubscriberStats
}

type state struct {
	journey rejseplanen.Journey
	at      time.Time
}

// SubscriberStats is reported on /healthz.
type SubscriberStats struct {
	Subscriptions int       `json:"subscriptions"`
	Messages      int       `json:"messages"`
	Updates       int       `json:"updates"`
	Tracked       int       `json:"tracked"`
	Errors        int       `json:"errors"`
	LastError     string    `json:"last_error,omitempty"`
	LastMessage   time.Time `json:"last_message"`
}

// ParseURL splits a Dataudveksleren AMQP URL into the Service Bus namespace,
// topic and subscription.
func ParseURL(raw string) (namespace, topic, subscription string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Scheme != "amqps" || u.Host == "" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("siri: want amqps://<namespace>/<topic>/<subscription>, got %q", raw)
	}
	return u.Host, parts[0], parts[1], nil
}

func (s *Subscriber) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Subscriber) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Run receives from every subscription until ctx is cancelled.
func (s *Subscriber) Run(ctx context.Context) error {
	type sub struct{ namespace, topic, name string }
	var subs []sub
	clients := map[string]*azservicebus.Client{}
	for _, raw := range s.URLs {
		ns, topic, name, err := ParseURL(raw)
		if err != nil {
			return err
		}
		if clients[ns] == nil {
			c, err := azservicebus.NewClient(ns, s.Credential, nil)
			if err != nil {
				return err
			}
			defer c.Close(context.Background())
			clients[ns] = c
		}
		subs = append(subs, sub{ns, topic, name})
	}
	s.mu.Lock()
	s.stats.Subscriptions = len(subs)
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, sb := range subs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.consume(ctx, clients[sb.namespace], sb.topic, sb.name)
		}()
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-t.C:
			s.prune()
		}
	}
}

// consume receives from one subscription, reconnecting with backoff.
func (s *Subscriber) consume(ctx context.Context, c *azservicebus.Client, topic, name string) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := s.receive(ctx, c, topic, name)
		if ctx.Err() != nil {
			return
		}
		s.fail(fmt.Errorf("%s: %w", topic, err))
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, time.Minute)
	}
}

func (s *Subscriber) receive(ctx context.Context, c *azservicebus.Client, topic, name string) error {
	// Realtime data is only worth having once; there is nothing to retry.
	r, err := c.NewReceiverForSubscription(topic, name, &azservicebus.ReceiverOptions{ReceiveMode: azservicebus.ReceiveModeReceiveAndDelete})
	if err != nil {
		return err
	}
	defer r.Close(context.Background())
	for {
		msgs, err := r.ReceiveMessages(ctx, 100, nil)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if err := s.Handle(m.Body); err != nil {
				s.fail(fmt.Errorf("%s: %w", topic, err))
			}
		}
	}
}

func (s *Subscriber) fail(err error) {
	s.log().Warn("siri subscription", "err", err)
	s.mu.Lock()
	s.stats.Errors++
	s.stats.LastError = err.Error()
	s.mu.Unlock()
	if s.Metrics != nil {
		s.Metrics.Inc("rpgtfsrt_siri_errors_total", "SIRI-ET receive and decode errors.")
	}
}

// Handle decodes one message and passes each journey in it, merged with what
// earlier messages said about it, to Store.
func (s *Subscriber) Handle(body []byte) error {
	now := s.now()
	updates := 0
	err := Decode(bytes.NewReader(body), func(u Update) {
		key := u.Journey.Key()
		at := u.RecordedAt
		if at.IsZero() || at.After(now) {
			at = now
		}
		s.mu.Lock()
		if s.journeys == nil {
			s.journeys = map[string]state{}
		}
		j := u.Journey
		if prev, ok := s.journeys[key]; ok {
			if at.Before(prev.at) {
				// A late message: keep the newer values.
				j, at = Merge(j, prev.journey), prev.at
			} else {
				j = Merge(prev.journey, j)
			}
		}
		s.journeys[key] = state{journey: j, at: at}
		s.mu.Unlock()
		s.Store.ObserveJourney(j, at)
		updates++
	})
	s.mu.Lock()
	s.stats.Messages++
	s.stats.Updates += updates
	s.stats.LastMessage = now
	s.mu.Unlock()
	if s.Metrics != nil {
		s.Metrics.Inc("rpgtfsrt_siri_messages_total", "SIRI-ET messages received.")
	}
	return err
}

func (s *Subscriber) prune() {
	now := s.now()
	s.mu.Lock()
	for k, st := range s.journeys {
		if now.Sub(st.at) > KeepJourney {
			delete(s.journeys, k)
		}
	}
	tracked := len(s.journeys)
	s.mu.Unlock()
	s.Store.Prune(now)
	if s.Metrics != nil {
		s.Metrics.Set("rpgtfsrt_siri_journeys", "Journeys tracked from SIRI-ET.", float64(tracked))
	}
}

func (s *Subscriber) Stats() SubscriberStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.Tracked = len(s.journeys)
	return st
}
