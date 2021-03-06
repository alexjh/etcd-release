package helpers

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type ErrorSet map[string]int

func (e ErrorSet) Error() string {
	message := "The following errors occurred:\n"
	for key, val := range e {
		message += fmt.Sprintf("  %s : %d\n", key, val)
	}
	return message
}

func (e ErrorSet) Add(err error) {
	e[err.Error()] = e[err.Error()] + 1
}

type kv interface {
	Address() string
	Set(key, value string) error
	Get(key string) (value string, err error)
}

type Spammer struct {
	kv                                 kv
	store                              map[string]string
	testConsumerConnectionErrorMessage string
	done                               chan struct{}
	wg                                 sync.WaitGroup
	intervalDuration                   time.Duration
	errors                             ErrorSet
	keyWriteAttempts                   int
}

func NewSpammer(kv kv, spamInterval time.Duration) *Spammer {
	address := strings.TrimPrefix(kv.Address(), "http://")
	message := fmt.Sprintf("dial tcp %s: getsockopt: connection refused", address)
	return &Spammer{
		testConsumerConnectionErrorMessage: message,
		kv:               kv,
		store:            make(map[string]string),
		done:             make(chan struct{}),
		intervalDuration: spamInterval,
		errors:           ErrorSet{},
	}
}

func (s *Spammer) Spam() {
	s.wg.Add(1)

	go func() {
		var counts struct {
			attempts int
		}
		for {
			select {
			case <-s.done:
				s.keyWriteAttempts = counts.attempts
				s.wg.Done()
				return
			case <-time.After(s.intervalDuration):
				counts.attempts++
				key := fmt.Sprintf("some-key-%d", counts.attempts-1)
				value := fmt.Sprintf("some-value-%d", counts.attempts-1)
				err := s.kv.Set(key, value)
				if err != nil {
					switch {
					case strings.Contains(err.Error(), s.testConsumerConnectionErrorMessage):
						// failures to connect to the test consumer should not count as failed key writes
						// this typically happens when the test-consumer vm is rolled
						counts.attempts--
					default:
						s.errors.Add(err)
					}
					continue
				}
				s.store[key] = value
			}
		}
	}()
}

func (s *Spammer) Stop() {
	close(s.done)
	s.wg.Wait()
}

func (s *Spammer) Check() error {
	if s.keyWriteAttempts == 0 {
		return errors.New("0 keys have been written")
	}
	for k, v := range s.store {
		value, err := s.kv.Get(k)
		if err != nil {
			s.errors.Add(err)
			break
		}

		if v != value {
			s.errors.Add(fmt.Errorf("value for key %q does not match: expected %q, got %q", k, v, value))
			break
		}
	}

	if len(s.errors) > 0 {
		return s.errors
	}

	return nil
}
