package commitmgr

import (
	"context"
	"sync"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"

	"github.com/dtynn/venus-cluster/venus-sealer/sealer/api"
)

type Batcher struct {
	ctx      context.Context
	mid      abi.ActorID
	ctrlAddr address.Address

	pendingCh chan api.SectorState

	force, stop chan struct{}

	processor Processor
}

func (b *Batcher) waitStop() {
	<-b.stop
}

func (b *Batcher) Add(sector api.SectorState) {
	b.pendingCh <- sector
}

func (b *Batcher) run() {
	timer := b.processor.CheckAfter(b.mid)
	wg := &sync.WaitGroup{}

	defer func() {
		wg.Wait()
		close(b.stop)
	}()

	pendingCap := b.processor.Threshold(b.mid)
	if pendingCap > 128 {
		pendingCap /= 4
	}

	pending := make([]api.SectorState, 0, pendingCap)

	for {
		tick, manual := false, false

		select {
		case <-b.ctx.Done():
			return
		case <-b.force:
			manual = true
		case <-timer.C:
			tick = true
		case s := <-b.pendingCh:
			pending = append(pending, s)
		}

		full := len(pending) >= b.processor.Threshold(b.mid)
		cleanAll := false
		if len(pending) > 0 {
			mlog := log.With("miner", b.mid)

			var processList []api.SectorState
			if full || manual || !b.processor.EnableBatch(b.mid) {
				mlog.Info("try to send all sector")
				processList = make([]api.SectorState, len(pending))
				copy(processList, pending)

				pending = pending[:0]

				cleanAll = true
			} else if tick {
				mlog.Info("tick tick! will send sectors which close to deadline")
				expired, err := b.processor.Expire(b.ctx, pending, b.mid)
				if err != nil {
					mlog.Warnf("check expired sectors: %s", err)
				}

				if len(expired) > 0 {
					remain := pending[:0]
					processList = make([]api.SectorState, 0, len(pending))
					for i := range pending {
						if _, ok := expired[pending[i].ID]; ok {
							processList = append(processList, pending[i])
						} else {
							remain = append(remain, pending[i])
						}
					}

					pending = remain
				}
			}

			if len(processList) > 0 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := b.processor.Process(b.ctx, processList, b.mid, b.ctrlAddr); err != nil {
						mlog.Errorf("process failed: %s", err)
					}
				}()
			}
		}

		if tick || cleanAll {
			timer.Stop()
			timer = b.processor.CheckAfter(b.mid)
		}
	}
}

func NewBatcher(ctx context.Context, mid abi.ActorID, ctrlAddr address.Address, processer Processor) *Batcher {
	b := &Batcher{
		ctx:       ctx,
		mid:       mid,
		ctrlAddr:  ctrlAddr,
		pendingCh: make(chan api.SectorState),
		force:     make(chan struct{}),
		stop:      make(chan struct{}),
		processor: processer,
	}
	go b.run()
	return b
}
