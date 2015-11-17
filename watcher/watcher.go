package watcher

import (
	"os"
	"time"

	"github.com/cloudfoundry-incubator/bbs"
	"github.com/cloudfoundry-incubator/bbs/events"
	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/tps/cc_client"
	"github.com/cloudfoundry/gunk/workpool"
	"github.com/pivotal-golang/lager"
)

type Watcher struct {
	bbsClient bbs.Client
	ccClient  cc_client.CcClient
	logger    lager.Logger

	pool *workpool.WorkPool
}

func NewWatcher(
	logger lager.Logger,
	workPoolSize int,
	bbsClient bbs.Client,
	ccClient cc_client.CcClient,
) (*Watcher, error) {
	workPool, err := workpool.NewWorkPool(workPoolSize)
	if err != nil {
		return nil, err
	}

	return &Watcher{
		bbsClient: bbsClient,
		ccClient:  ccClient,
		logger:    logger,

		pool: workPool,
	}, nil
}

func (watcher *Watcher) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	logger := watcher.logger.Session("watcher")
	logger.Info("starting")
	defer logger.Info("finished")

	var subscription events.EventSource
	subscriptionChan := make(chan events.EventSource, 1)
	go subscribeToEvents(logger, watcher.bbsClient, subscriptionChan)

	eventChan := make(chan models.Event, 1)
	nextErrCount := 0

	close(ready)
	logger.Info("started")

	for {
		select {
		case subscription = <-subscriptionChan:
			if subscription != nil {
				go nextEvent(logger, subscription, eventChan)
			} else {
				go subscribeToEvents(logger, watcher.bbsClient, subscriptionChan)
			}

		case event := <-eventChan:
			if event != nil {
				watcher.handleEvent(logger, event)
			} else {
				nextErrCount += 1
				if nextErrCount > 2 {
					nextErrCount = 0
					go subscribeToEvents(logger, watcher.bbsClient, subscriptionChan)
					break
				}
			}
			go nextEvent(logger, subscription, eventChan)

		case <-signals:
			logger.Info("stopping")
			err := subscription.Close()
			if err != nil {
				logger.Error("failed-closing-event-source", err)
			}
			return nil
		}
	}
}

func (watcher *Watcher) handleEvent(logger lager.Logger, event models.Event) {
	if changed, ok := event.(*models.ActualLRPChangedEvent); ok {
		after, _ := changed.After.Resolve()

		if after.Domain == cc_messages.AppLRPDomain {
			before, _ := changed.Before.Resolve()

			if after.CrashCount > before.CrashCount {
				logger.Info("app-crashed", lager.Data{
					"process-guid": after.ProcessGuid,
					"index":        after.Index,
				})

				guid := after.ProcessGuid
				appCrashed := cc_messages.AppCrashedRequest{
					Instance:        before.InstanceGuid,
					Index:           int(after.Index),
					Reason:          "CRASHED",
					ExitDescription: after.CrashReason,
					CrashCount:      int(after.CrashCount),
					CrashTimestamp:  after.Since,
				}

				watcher.pool.Submit(func() {
					logger := logger.WithData(lager.Data{
						"process-guid": guid,
						"index":        appCrashed.Index,
					})
					logger.Info("recording-app-crashed")
					err := watcher.ccClient.AppCrashed(guid, appCrashed, logger)
					if err != nil {
						logger.Error("failed-recording-app-crashed", err)
					}
				})
			}
		}
	}
}

func subscribeToEvents(logger lager.Logger, bbsClient bbs.Client, subscriptionChan chan<- events.EventSource) {
	logger.Info("subscribing-to-events")
	eventSource, err := bbsClient.SubscribeToEvents()
	if err != nil {
		logger.Error("failed-subscribing-to-events", err)
		subscriptionChan <- nil
	} else {
		logger.Info("subscribed-to-events")
		subscriptionChan <- eventSource
	}
}

func nextEvent(logger lager.Logger, es events.EventSource, eventChan chan<- models.Event) {
	event, err := es.Next()

	switch err {
	case nil:
		eventChan <- event

	case events.ErrSourceClosed:
		return

	default:
		logger.Error("failed-getting-next-event", err)
		// wait a bit before retrying
		time.Sleep(time.Second)
		eventChan <- nil
	}
}
