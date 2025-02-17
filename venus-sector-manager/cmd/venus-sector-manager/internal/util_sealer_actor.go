package internal

import (
	"bytes"
	"fmt"
	"os"

	cbor "github.com/ipfs/go-ipld-cbor"
	"github.com/urfave/cli/v2"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-bitfield"
	rlepluslazy "github.com/filecoin-project/go-bitfield/rle"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/network"

	miner2 "github.com/filecoin-project/specs-actors/v2/actors/builtin/miner"

	"github.com/filecoin-project/venus/app/submodule/chain"
	"github.com/filecoin-project/venus/venus-shared/actors"
	"github.com/filecoin-project/venus/venus-shared/actors/adt"
	"github.com/filecoin-project/venus/venus-shared/actors/builtin/miner"
	"github.com/filecoin-project/venus/venus-shared/types"

	"github.com/ipfs-force-community/venus-cluster/venus-sector-manager/modules/policy"
)

var utilSealerActorCmd = &cli.Command{
	Name:  "actor",
	Usage: "Manipulate the miner actor",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name: "miner",
		},
	},
	Subcommands: []*cli.Command{
		utilSealerActorWithdrawCmd,
		utilSealerActorRepayDebtCmd,
		utilSealerActorSetOwnerCmd,
		utilSealerActorControl,
		utilSealerActorProposeChangeWorker,
		utilSealerActorConfirmChangeWorker,
		utilSealerActorCompactAllocatedCmd,
	},
}

var utilSealerActorWithdrawCmd = &cli.Command{
	Name:      "withdraw",
	Usage:     "withdraw available balance",
	ArgsUsage: "[amount (FIL)]",
	Flags: []cli.Flag{
		&cli.IntFlag{
			Name:  "confidence",
			Usage: "number of block confirmations to wait for",
			Value: int(policy.InteractivePoRepConfidence),
		},
	},
	Action: func(cctx *cli.Context) error {
		api, ctx, stop, err := extractAPI(cctx)
		if err != nil {
			return err
		}
		defer stop()

		maddr, err := getMinerActorAddress(cctx.String("miner"))
		if err != nil {
			return err
		}

		mi, err := api.Chain.StateMinerInfo(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		available, err := api.Chain.StateMinerAvailableBalance(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		amount := available
		if cctx.Args().Present() {
			f, err := types.ParseFIL(cctx.Args().First())
			if err != nil {
				return xerrors.Errorf("parsing 'amount' argument: %w", err)
			}

			amount = abi.TokenAmount(f)

			if amount.GreaterThan(available) {
				return xerrors.Errorf("can't withdraw more funds than available; requested: %s; available: %s", types.FIL(amount), types.FIL(available))
			}
		}

		params, err := actors.SerializeParams(&miner2.WithdrawBalanceParams{
			AmountRequested: amount, // Default to attempting to withdraw all the extra funds in the miner actor
		})
		if err != nil {
			return err
		}

		mid, err := api.Messager.PushMessage(ctx, &types.Message{
			To:     maddr,
			From:   mi.Owner,
			Value:  types.NewInt(0),
			Method: miner.Methods.WithdrawBalance,
			Params: params,
		}, nil)
		if err != nil {
			return err
		}

		fmt.Printf("Requested rewards withdrawal in message %s\n", mid)

		// wait for it to get mined into a block
		fmt.Printf("waiting for %d epochs for confirmation..\n", uint64(cctx.Int("confidence")))

		wait, err := api.Messager.WaitMessage(ctx, mid, uint64(cctx.Int("confidence")))
		if err != nil {
			return err
		}

		// check it executed successfully
		if wait.Receipt.ExitCode != 0 {
			fmt.Println(cctx.App.Writer, "withdrawal failed!")
			return err
		}

		nv, err := api.Chain.StateNetworkVersion(ctx, wait.TipSetKey)
		if err != nil {
			return err
		}

		if nv >= network.Version14 {
			var withdrawn abi.TokenAmount
			if err := withdrawn.UnmarshalCBOR(bytes.NewReader(wait.Receipt.Return)); err != nil {
				return err
			}

			fmt.Printf("Successfully withdrew %s \n", types.FIL(withdrawn))
			if withdrawn.LessThan(amount) {
				fmt.Printf("Note that this is less than the requested amount of %s\n", types.FIL(amount))
			}
		}

		return nil
	},
}

var utilSealerActorRepayDebtCmd = &cli.Command{
	Name:      "repay-debt",
	Usage:     "pay down a miner's debt",
	ArgsUsage: "[amount (FIL)]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "from",
			Usage: "optionally specify the account to send funds from",
		},
	},
	Action: func(cctx *cli.Context) error {
		api, ctx, stop, err := extractAPI(cctx)
		if err != nil {
			return err
		}
		defer stop()

		maddr, err := getMinerActorAddress(cctx.String("miner"))
		if err != nil {
			return err
		}

		mi, err := api.Chain.StateMinerInfo(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		var amount abi.TokenAmount
		if cctx.Args().Present() {
			f, err := types.ParseFIL(cctx.Args().First())
			if err != nil {
				return xerrors.Errorf("parsing 'amount' argument: %w", err)
			}

			amount = abi.TokenAmount(f)
		} else {
			mact, err := api.Chain.StateGetActor(ctx, maddr, types.EmptyTSK)
			if err != nil {
				return err
			}

			store := adt.WrapStore(ctx, cbor.NewCborStore(chain.NewAPIBlockstore(api.Chain)))
			mst, err := miner.Load(store, mact)
			if err != nil {
				return err
			}

			amount, err = mst.FeeDebt()
			if err != nil {
				return err
			}
		}

		fromAddr := mi.Worker
		if from := cctx.String("from"); from != "" {
			addr, err := address.NewFromString(from)
			if err != nil {
				return err
			}

			fromAddr = addr
		}

		fromId, err := api.Chain.StateLookupID(ctx, fromAddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		if !mi.IsController(fromId) {
			return xerrors.Errorf("sender isn't a controller of miner: %s", fromId)
		}

		mid, err := api.Messager.PushMessage(ctx, &types.Message{
			To:     maddr,
			From:   fromId,
			Value:  amount,
			Method: miner.Methods.RepayDebt,
			Params: nil,
		}, nil)
		if err != nil {
			return err
		}

		fmt.Printf("Sent repay debt message %s\n", mid)

		return nil
	},
}

var utilSealerActorControl = &cli.Command{
	Name:  "control",
	Usage: "Manage control addresses",
	Subcommands: []*cli.Command{
		utilSealerActorControlList,
		utilSealerActorControlSet,
	},
}

var utilSealerActorControlList = &cli.Command{
	Name:  "list",
	Usage: "Get currently set control addresses",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name: "verbose",
		},
		&cli.BoolFlag{
			Name:        "color",
			Usage:       "use color in display output",
			DefaultText: "depends on output being a TTY",
		},
	},
	Action: func(cctx *cli.Context) error {
		api, ctx, stop, err := extractAPI(cctx)
		if err != nil {
			return err
		}
		defer stop()

		maddr, err := getMinerActorAddress(cctx.String("miner"))
		if err != nil {
			return err
		}

		mi, err := api.Chain.StateMinerInfo(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		fmt.Fprintln(os.Stdout, "Owner:")
		fmt.Fprintf(os.Stdout, "\t%s\n", mi.Owner.String())

		fmt.Fprintln(os.Stdout, "Worker:")
		fmt.Fprintf(os.Stdout, "\t%s\n", mi.Worker.String())

		fmt.Fprintln(os.Stdout, "Control:")
		for _, ca := range mi.ControlAddresses {
			fmt.Fprintf(os.Stdout, "\t%s\n", ca.String())
		}

		return nil
	},
}

var utilSealerActorControlSet = &cli.Command{
	Name:      "set",
	Usage:     "Set control address(-es)",
	ArgsUsage: "[...address]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "really-do-it",
			Usage: "Actually send transaction performing the action",
			Value: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		api, ctx, stop, err := extractAPI(cctx)
		if err != nil {
			return err
		}
		defer stop()

		maddr, err := getMinerActorAddress(cctx.String("miner"))
		if err != nil {
			return err
		}

		mi, err := api.Chain.StateMinerInfo(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		del := map[address.Address]struct{}{}
		existing := map[address.Address]struct{}{}
		for _, controlAddress := range mi.ControlAddresses {
			ka, err := api.Chain.StateAccountKey(ctx, controlAddress, types.EmptyTSK)
			if err != nil {
				return err
			}

			del[ka] = struct{}{}
			existing[ka] = struct{}{}
		}

		var toSet []address.Address

		for i, as := range cctx.Args().Slice() {
			a, err := address.NewFromString(as)
			if err != nil {
				return xerrors.Errorf("parsing address %d: %w", i, err)
			}

			ka, err := api.Chain.StateAccountKey(ctx, a, types.EmptyTSK)
			if err != nil {
				return err
			}

			// make sure the address exists on chain
			_, err = api.Chain.StateLookupID(ctx, ka, types.EmptyTSK)
			if err != nil {
				return xerrors.Errorf("looking up %s: %w", ka, err)
			}

			delete(del, ka)
			toSet = append(toSet, ka)
		}

		for a := range del {
			fmt.Println("Remove", a)
		}
		for _, a := range toSet {
			if _, exists := existing[a]; !exists {
				fmt.Println("Add", a)
			}
		}

		if !cctx.Bool("really-do-it") {
			fmt.Println("Pass --really-do-it to actually execute this action")
			return nil
		}

		cwp := &miner2.ChangeWorkerAddressParams{
			NewWorker:       mi.Worker,
			NewControlAddrs: toSet,
		}

		sp, err := actors.SerializeParams(cwp)
		if err != nil {
			return xerrors.Errorf("serializing params: %w", err)
		}

		mid, err := api.Messager.PushMessage(ctx, &types.Message{
			From:   mi.Owner,
			To:     maddr,
			Method: miner.Methods.ChangeWorkerAddress,

			Value:  big.Zero(),
			Params: sp,
		}, nil)
		if err != nil {
			return xerrors.Errorf("push message: %w", err)
		}

		fmt.Println("Message ID:", mid)

		return nil
	},
}

var utilSealerActorSetOwnerCmd = &cli.Command{
	Name:      "set-owner",
	Usage:     "Set owner address (this command should be invoked twice, first with the old owner as the senderAddress, and then with the new owner)",
	ArgsUsage: "[newOwnerAddress senderAddress]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "really-do-it",
			Usage: "Actually send transaction performing the action",
			Value: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.Bool("really-do-it") {
			fmt.Println("Pass --really-do-it to actually execute this action")
			return nil
		}

		if cctx.NArg() != 2 {
			return fmt.Errorf("must pass new owner address and sender address")
		}

		api, ctx, stop, err := extractAPI(cctx)
		if err != nil {
			return err
		}
		defer stop()

		maddr, err := getMinerActorAddress(cctx.String("miner"))
		if err != nil {
			return err
		}

		na, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		newAddrId, err := api.Chain.StateLookupID(ctx, na, types.EmptyTSK)
		if err != nil {
			return err
		}

		fa, err := address.NewFromString(cctx.Args().Get(1))
		if err != nil {
			return err
		}

		fromAddrId, err := api.Chain.StateLookupID(ctx, fa, types.EmptyTSK)
		if err != nil {
			return err
		}

		mi, err := api.Chain.StateMinerInfo(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		if fromAddrId != mi.Owner && fromAddrId != newAddrId {
			return xerrors.New("from address must either be the old owner or the new owner")
		}

		sp, err := actors.SerializeParams(&newAddrId)
		if err != nil {
			return xerrors.Errorf("serializing params: %w", err)
		}

		mid, err := api.Messager.PushMessage(ctx, &types.Message{
			From:   fromAddrId,
			To:     maddr,
			Method: miner.Methods.ChangeOwnerAddress,
			Value:  big.Zero(),
			Params: sp,
		}, nil)
		if err != nil {
			return xerrors.Errorf("push message: %w", err)
		}

		fmt.Println("Message ID:", mid)

		// wait for it to get mined into a block
		wait, err := api.Messager.WaitMessage(ctx, mid, policy.InteractivePoRepConfidence)
		if err != nil {
			return err
		}

		// check it executed successfully
		if wait.Receipt.ExitCode != 0 {
			fmt.Println("owner change failed!")
			return err
		}

		fmt.Println("message succeeded!")

		return nil
	},
}

var utilSealerActorProposeChangeWorker = &cli.Command{
	Name:      "propose-change-worker",
	Usage:     "Propose a worker address change",
	ArgsUsage: "[address]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "really-do-it",
			Usage: "Actually send transaction performing the action",
			Value: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return fmt.Errorf("must pass address of new worker address")
		}

		api, ctx, stop, err := extractAPI(cctx)
		if err != nil {
			return err
		}
		defer stop()

		maddr, err := getMinerActorAddress(cctx.String("miner"))
		if err != nil {
			return err
		}

		na, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		newAddr, err := api.Chain.StateLookupID(ctx, na, types.EmptyTSK)
		if err != nil {
			return err
		}

		mi, err := api.Chain.StateMinerInfo(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		if mi.NewWorker.Empty() {
			if mi.Worker == newAddr {
				return fmt.Errorf("worker address already set to %s", na)
			}
		} else {
			if mi.NewWorker == newAddr {
				return fmt.Errorf("change to worker address %s already pending", na)
			}
		}

		if !cctx.Bool("really-do-it") {
			fmt.Println("Pass --really-do-it to actually execute this action")
			return nil
		}

		cwp := &miner2.ChangeWorkerAddressParams{
			NewWorker:       newAddr,
			NewControlAddrs: mi.ControlAddresses,
		}

		sp, err := actors.SerializeParams(cwp)
		if err != nil {
			return xerrors.Errorf("serializing params: %w", err)
		}

		mid, err := api.Messager.PushMessage(ctx, &types.Message{
			From:   mi.Owner,
			To:     maddr,
			Method: miner.Methods.ChangeWorkerAddress,
			Value:  big.Zero(),
			Params: sp,
		}, nil)
		if err != nil {
			return xerrors.Errorf("push message: %w", err)
		}

		fmt.Println("Propose Message CID:", mid)

		// wait for it to get mined into a block
		wait, err := api.Messager.WaitMessage(ctx, mid, policy.InteractivePoRepConfidence)
		if err != nil {
			return err
		}

		// check it executed successfully
		if wait.Receipt.ExitCode != 0 {
			fmt.Println("Propose worker change failed!")
			return err
		}

		mi, err = api.Chain.StateMinerInfo(ctx, maddr, wait.TipSetKey)
		if err != nil {
			return err
		}
		if mi.NewWorker != newAddr {
			return fmt.Errorf("proposed worker address change not reflected on chain: expected %s, found %s", na, mi.NewWorker)
		}

		fmt.Printf("Worker key change to %s successfully proposed.\n", na)
		fmt.Printf("Call 'confirm-change-worker' at or after height %d to complete.\n", mi.WorkerChangeEpoch)

		return nil
	},
}

var utilSealerActorConfirmChangeWorker = &cli.Command{
	Name:      "confirm-change-worker",
	Usage:     "Confirm a worker address change",
	ArgsUsage: "[address]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "really-do-it",
			Usage: "Actually send transaction performing the action",
			Value: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.Args().Present() {
			return fmt.Errorf("must pass address of new worker address")
		}

		api, ctx, stop, err := extractAPI(cctx)
		if err != nil {
			return err
		}
		defer stop()

		maddr, err := getMinerActorAddress(cctx.String("miner"))
		if err != nil {
			return err
		}

		na, err := address.NewFromString(cctx.Args().First())
		if err != nil {
			return err
		}

		newAddr, err := api.Chain.StateLookupID(ctx, na, types.EmptyTSK)
		if err != nil {
			return err
		}

		mi, err := api.Chain.StateMinerInfo(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		if mi.NewWorker.Empty() {
			return xerrors.Errorf("no worker key change proposed")
		} else if mi.NewWorker != newAddr {
			return xerrors.Errorf("worker key %s does not match current worker key proposal %s", newAddr, mi.NewWorker)
		}

		if head, err := api.Chain.ChainHead(ctx); err != nil {
			return xerrors.Errorf("failed to get the chain head: %w", err)
		} else if head.Height() < mi.WorkerChangeEpoch {
			return xerrors.Errorf("worker key change cannot be confirmed until %d, current height is %d", mi.WorkerChangeEpoch, head.Height())
		}

		if !cctx.Bool("really-do-it") {
			fmt.Println("Pass --really-do-it to actually execute this action")
			return nil
		}

		mid, err := api.Messager.PushMessage(ctx, &types.Message{
			From:   mi.Owner,
			To:     maddr,
			Method: miner.Methods.ConfirmUpdateWorkerKey,
			Value:  big.Zero(),
		}, nil)
		if err != nil {
			return xerrors.Errorf("push message: %w", err)
		}

		fmt.Println("Confirm Message ID:", mid)

		// wait for it to get mined into a block
		wait, err := api.Messager.WaitMessage(ctx, mid, policy.InteractivePoRepConfidence)
		if err != nil {
			return err
		}

		// check it executed successfully
		if wait.Receipt.ExitCode != 0 {
			fmt.Println("Worker change execute failed!")
			return err
		}

		mi, err = api.Chain.StateMinerInfo(ctx, maddr, wait.TipSetKey)
		if err != nil {
			return err
		}
		if mi.Worker != newAddr {
			return fmt.Errorf("confirmed worker address change not reflected on chain: expected '%s', found '%s'", newAddr, mi.Worker)
		}

		return nil
	},
}

var utilSealerActorCompactAllocatedCmd = &cli.Command{
	Name:  "compact-allocated",
	Usage: "compact allocated sectors bitfield",
	Flags: []cli.Flag{
		&cli.Uint64Flag{
			Name:  "mask-last-offset",
			Usage: "Mask sector IDs from 0 to 'higest_allocated - offset'",
		},
		&cli.Uint64Flag{
			Name:  "mask-upto-n",
			Usage: "Mask sector IDs from 0 to 'n'",
		},
		&cli.BoolFlag{
			Name:  "really-do-it",
			Usage: "Actually send transaction performing the action",
			Value: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.Bool("really-do-it") {
			fmt.Println("Pass --really-do-it to actually execute this action")
			return nil
		}

		if !cctx.Args().Present() {
			return fmt.Errorf("must pass address of new owner address")
		}

		api, ctx, stop, err := extractAPI(cctx)
		if err != nil {
			return err
		}
		defer stop()

		maddr, err := getMinerActorAddress(cctx.String("miner"))
		if err != nil {
			return err
		}

		mact, err := api.Chain.StateGetActor(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		store := adt.WrapStore(ctx, cbor.NewCborStore(chain.NewAPIBlockstore(api.Chain)))

		mst, err := miner.Load(store, mact)
		if err != nil {
			return err
		}

		allocs, err := mst.GetAllocatedSectors()
		if err != nil {
			return err
		}

		var maskBf bitfield.BitField

		{
			exclusiveFlags := []string{"mask-last-offset", "mask-upto-n"}
			hasFlag := false
			for _, f := range exclusiveFlags {
				if hasFlag && cctx.IsSet(f) {
					return xerrors.Errorf("more than one 'mask` flag set")
				}
				hasFlag = hasFlag || cctx.IsSet(f)
			}
		}
		switch {
		case cctx.IsSet("mask-last-offset"):
			last, err := allocs.Last()
			if err != nil {
				return err
			}

			m := cctx.Uint64("mask-last-offset")
			if last <= m+1 {
				return xerrors.Errorf("highest allocated sector lower than mask offset %d: %d", m+1, last)
			}
			// securty to not brick a miner
			if last > 1<<60 {
				return xerrors.Errorf("very high last sector number, refusing to mask: %d", last)
			}

			maskBf, err = bitfield.NewFromIter(&rlepluslazy.RunSliceIterator{
				Runs: []rlepluslazy.Run{{Val: true, Len: last - m}}})
			if err != nil {
				return xerrors.Errorf("forming bitfield: %w", err)
			}
		case cctx.IsSet("mask-upto-n"):
			n := cctx.Uint64("mask-upto-n")
			maskBf, err = bitfield.NewFromIter(&rlepluslazy.RunSliceIterator{
				Runs: []rlepluslazy.Run{{Val: true, Len: n}}})
			if err != nil {
				return xerrors.Errorf("forming bitfield: %w", err)
			}
		default:
			return xerrors.Errorf("no 'mask' flags set")
		}

		mi, err := api.Chain.StateMinerInfo(ctx, maddr, types.EmptyTSK)
		if err != nil {
			return err
		}

		params := &miner2.CompactSectorNumbersParams{
			MaskSectorNumbers: maskBf,
		}

		sp, err := actors.SerializeParams(params)
		if err != nil {
			return xerrors.Errorf("serializing params: %w", err)
		}

		mid, err := api.Messager.PushMessage(ctx, &types.Message{
			From:   mi.Worker,
			To:     maddr,
			Method: miner.Methods.CompactSectorNumbers,
			Value:  big.Zero(),
			Params: sp,
		}, nil)
		if err != nil {
			return xerrors.Errorf("mpool push: %w", err)
		}

		fmt.Println("CompactSectorNumbers Message ID:", mid)

		// wait for it to get mined into a block
		wait, err := api.Messager.WaitMessage(ctx, mid, policy.InteractivePoRepConfidence)
		if err != nil {
			return err
		}

		// check it executed successfully
		if wait.Receipt.ExitCode != 0 {
			fmt.Println("Propose owner change execute failed")
			return err
		}

		return nil
	},
}
