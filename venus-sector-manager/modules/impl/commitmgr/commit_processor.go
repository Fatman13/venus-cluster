package commitmgr

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-bitfield"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"

	miner5 "github.com/filecoin-project/specs-actors/v5/actors/builtin/miner"
	proof5 "github.com/filecoin-project/specs-actors/v5/actors/runtime/proof"

	"github.com/filecoin-project/venus/venus-shared/actors/builtin"
	"github.com/filecoin-project/venus/venus-shared/actors/builtin/miner"

	"github.com/ipfs-force-community/venus-cluster/venus-sector-manager/api"
	"github.com/ipfs-force-community/venus-cluster/venus-sector-manager/modules"
	"github.com/ipfs-force-community/venus-cluster/venus-sector-manager/pkg/logging"
	"github.com/ipfs-force-community/venus-cluster/venus-sector-manager/pkg/messager"
)

type CommitProcessor struct {
	api       SealingAPI
	msgClient messager.API

	smgr api.SectorStateManager

	config *modules.SafeConfig

	prover api.Prover
}

func (c CommitProcessor) processIndividually(ctx context.Context, sectors []api.SectorState, from address.Address, mid abi.ActorID, plog *logging.ZapLogger) {
	mcfg := c.config.MustMinerConfig(mid)

	var spec messager.MsgMeta
	spec.GasOverEstimation = mcfg.Commitment.Prove.GasOverEstimation
	spec.MaxFeeCap = mcfg.Commitment.Prove.MaxFeeCap.Std()

	wg := sync.WaitGroup{}
	wg.Add(len(sectors))
	for i := range sectors {
		go func(idx int) {
			slog := plog.With("sector", sectors[idx].ID.Number)

			defer wg.Done()

			params := &miner.ProveCommitSectorParams{
				SectorNumber: sectors[idx].ID.Number,
				Proof:        sectors[idx].Proof.Proof,
			}

			enc := new(bytes.Buffer)
			if err := params.MarshalCBOR(enc); err != nil {
				slog.Error("serialize commit sector parameters failed: ", err)
				return
			}

			tok, _, err := c.api.ChainHead(ctx)
			if err != nil {
				slog.Error("get chain head: ", err)
				return
			}

			collateral, err := getSectorCollateral(ctx, c.api, mid, sectors[idx].ID.Number, tok)
			if err != nil {
				slog.Error("get sector collateral failed: ", err)
				return
			}

			mcid, err := pushMessage(ctx, from, mid, collateral, miner.Methods.ProveCommitSector, c.msgClient, spec, enc.Bytes(), slog)
			if err != nil {
				slog.Error("push commit single failed: ", err)
				return
			}

			sectors[idx].MessageInfo.CommitCid = &mcid
			slog.Info("push commit success, cid: ", mcid)
		}(i)
	}
	wg.Wait()
}

func (c CommitProcessor) Process(ctx context.Context, sectors []api.SectorState, mid abi.ActorID, ctrlAddr address.Address) error {
	// Notice: If a sector in sectors has been sent, it's cid failed should be changed already.
	plog := log.With("proc", "prove", "miner", mid, "ctrl", ctrlAddr.String(), "len", len(sectors))

	start := time.Now()
	defer plog.Infof("finished process, elasped %s", time.Since(start))

	defer updateSector(ctx, c.smgr, sectors, plog)

	if !c.EnableBatch(mid) || len(sectors) < miner5.MinAggregatedSectors {
		c.processIndividually(ctx, sectors, ctrlAddr, mid, plog)
		return nil
	}

	tok, _, err := c.api.ChainHead(ctx)
	if err != nil {
		return fmt.Errorf("get chain head failed: %w", err)
	}

	infos := []proof5.AggregateSealVerifyInfo{}
	sectorsMap := map[abi.SectorNumber]api.SectorState{}
	failed := map[abi.SectorID]struct{}{}

	collateral := big.Zero()
	for i, p := range sectors {
		sectorsMap[p.ID.Number] = sectors[i]
		sc, err := getSectorCollateral(ctx, c.api, mid, p.ID.Number, tok)
		if err != nil {
			plog.Errorf("get sector collateral for %d failed: %s\n", p.ID.Number, err)
			failed[sectors[i].ID] = struct{}{}
			continue
		}

		collateral = big.Add(collateral, sc)

		infos = append(infos, proof5.AggregateSealVerifyInfo{
			Number:                p.ID.Number,
			Randomness:            abi.SealRandomness(p.Ticket.Ticket),
			InteractiveRandomness: abi.InteractiveSealRandomness(p.Seed.Seed),
			SealedCID:             p.Pre.CommR,
			UnsealedCID:           p.Pre.CommD,
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Number < infos[j].Number
	})

	params := &miner.ProveCommitAggregateParams{
		SectorNumbers: bitfield.New(),
	}

	proofs := make([][]byte, 0)
	for i := range infos {
		params.SectorNumbers.Set(uint64(infos[i].Number))

		proofs = append(proofs, sectorsMap[infos[i].Number].Proof.Proof)
	}

	params.AggregateProof, err = c.prover.AggregateSealProofs(ctx, proof5.AggregateSealVerifyProofAndInfos{
		Miner:          mid,
		SealProof:      sectorsMap[infos[0].Number].SectorType,
		AggregateProof: abi.RegisteredAggregationProof_SnarkPackV1,
		Infos:          infos,
	}, proofs)

	if err != nil {
		return fmt.Errorf("aggregate sector failed: %w", err)
	}

	enc := new(bytes.Buffer)
	if err := params.MarshalCBOR(enc); err != nil {
		return fmt.Errorf("couldn't serialize ProveCommitAggregateParams: %w", err)
	}

	mcfg := c.config.MustMinerConfig(mid)

	var spec messager.MsgMeta
	spec.GasOverEstimation = mcfg.Commitment.Prove.GasOverEstimation
	spec.MaxFeeCap = mcfg.Commitment.Prove.MaxFeeCap.Std()

	ccid, err := pushMessage(ctx, ctrlAddr, mid, collateral, miner.Methods.ProveCommitAggregate,
		c.msgClient, spec, enc.Bytes(), plog)
	if err != nil {
		return fmt.Errorf("push aggregate prove message failed: %w", err)
	}

	for i := range sectors {
		if _, ok := failed[sectors[i].ID]; !ok {
			sectors[i].MessageInfo.CommitCid = &ccid
		}
	}

	return nil
}

func (c CommitProcessor) Expire(ctx context.Context, sectors []api.SectorState, mid abi.ActorID) (map[abi.SectorID]struct{}, error) {
	maxWait := c.config.MustMinerConfig(mid).Commitment.Prove.Batch.MaxWait.Std()
	maxWaitHeight := abi.ChainEpoch(maxWait / (builtin.EpochDurationSeconds * time.Second))
	_, h, err := c.api.ChainHead(ctx)
	if err != nil {
		return nil, err
	}
	expire := map[abi.SectorID]struct{}{}
	for _, s := range sectors {
		if h-s.Seed.Epoch > maxWaitHeight {
			expire[s.ID] = struct{}{}
		}
	}
	return expire, nil
}

func (c CommitProcessor) CheckAfter(mid abi.ActorID) *time.Timer {
	return time.NewTimer(c.config.MustMinerConfig(mid).Commitment.Prove.Batch.CheckInterval.Std())
}

func (c CommitProcessor) Threshold(mid abi.ActorID) int {
	return c.config.MustMinerConfig(mid).Commitment.Prove.Batch.Threshold
}

func (c CommitProcessor) EnableBatch(mid abi.ActorID) bool {
	return c.config.MustMinerConfig(mid).Commitment.Prove.Batch.Enabled
}

var _ Processor = (*CommitProcessor)(nil)
