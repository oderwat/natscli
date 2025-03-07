// Copyright 2019 The NATS Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/choria-io/fisk"
	"github.com/dustin/go-humanize"
	"github.com/google/go-cmp/cmp"
	"github.com/nats-io/jsm.go/api"
	"github.com/nats-io/nats.go"

	"github.com/nats-io/jsm.go"
)

type consumerCmd struct {
	consumer       string
	stream         string
	json           bool
	listNames      bool
	force          bool
	ack            bool
	ackSetByUser   bool
	term           bool
	raw            bool
	destination    string
	inputFile      string
	outFile        string
	showAll        bool
	acceptDefaults bool

	selectedConsumer *jsm.Consumer

	ackPolicy           string
	ackWait             time.Duration
	bpsRateLimit        uint64
	delivery            string
	ephemeral           bool
	filterSubjects      []string
	idleHeartbeat       string
	maxAckPending       int
	maxDeliver          int
	maxWaiting          int
	deliveryGroup       string
	pull                bool
	pullCount           int
	replayPolicy        string
	reportLeaderDistrib bool
	samplePct           int
	startPolicy         string
	validateOnly        bool
	description         string
	inactiveThreshold   time.Duration
	maxPullExpire       time.Duration
	maxPullBytes        int
	maxPullBatch        int
	backoffMode         string
	backoffSteps        uint
	backoffMin          time.Duration
	backoffMax          time.Duration
	replicas            int
	memory              bool
	hdrsOnly            bool
	hdrsOnlySet         bool
	fc                  bool
	fcSet               bool
	metadataIsSet       bool
	metadata            map[string]string

	dryRun bool
	mgr    *jsm.Manager
	nc     *nats.Conn
}

func configureConsumerCommand(app commandHost) {
	c := &consumerCmd{metadata: map[string]string{}}

	addCreateFlags := func(f *fisk.CmdClause, edit bool) {
		if !edit {
			f.Flag("ack", "Acknowledgment policy (none, all, explicit)").StringVar(&c.ackPolicy)
			f.Flag("bps", "Restrict message delivery to a certain bit per second").Default("0").Uint64Var(&c.bpsRateLimit)
		}
		f.Flag("backoff", "Creates a consumer backoff policy using a specific pre-written algorithm (none, linear)").PlaceHolder("MODE").EnumVar(&c.backoffMode, "linear", "none")
		f.Flag("backoff-steps", "Number of steps to use when creating the backoff policy").PlaceHolder("STEPS").Default("10").UintVar(&c.backoffSteps)
		f.Flag("backoff-min", "The shortest backoff period that will be generated").PlaceHolder("MIN").Default("1m").DurationVar(&c.backoffMin)
		f.Flag("backoff-max", "The longest backoff period that will be generated").PlaceHolder("MAX").Default("20m").DurationVar(&c.backoffMax)
		if !edit {
			f.Flag("deliver", "Start policy (all, new, last, subject, 1h, msg sequence)").PlaceHolder("POLICY").StringVar(&c.startPolicy)
			f.Flag("deliver-group", "Delivers push messages only to subscriptions matching this group").Default("_unset_").PlaceHolder("GROUP").StringVar(&c.deliveryGroup)
		}
		f.Flag("description", "Sets a contextual description for the consumer").StringVar(&c.description)
		if !edit {
			f.Flag("ephemeral", "Create an ephemeral Consumer").UnNegatableBoolVar(&c.ephemeral)
		}
		f.Flag("filter", "Filter Stream by subjects").PlaceHolder("SUBJECTS").StringsVar(&c.filterSubjects)
		if !edit {
			f.Flag("flow-control", "Enable Push consumer flow control").IsSetByUser(&c.fcSet).UnNegatableBoolVar(&c.fc)
			f.Flag("heartbeat", "Enable idle Push consumer heartbeats (-1 disable)").StringVar(&c.idleHeartbeat)
		}

		f.Flag("headers-only", "Deliver only headers and no bodies").IsSetByUser(&c.hdrsOnlySet).BoolVar(&c.hdrsOnly)
		f.Flag("max-deliver", "Maximum amount of times a message will be delivered").PlaceHolder("TRIES").IntVar(&c.maxDeliver)
		f.Flag("max-outstanding", "Maximum pending Acks before consumers are paused").Hidden().Default("-1").IntVar(&c.maxAckPending)
		f.Flag("max-pending", "Maximum pending Acks before consumers are paused").Default("-1").IntVar(&c.maxAckPending)
		f.Flag("max-waiting", "Maximum number of outstanding pulls allowed").PlaceHolder("PULLS").IntVar(&c.maxWaiting)
		f.Flag("max-pull-batch", "Maximum size batch size for a pull request to accept").PlaceHolder("BATCH_SIZE").IntVar(&c.maxPullBatch)
		f.Flag("max-pull-expire", "Maximum expire duration for a pull request to accept").PlaceHolder("EXPIRES").DurationVar(&c.maxPullExpire)
		f.Flag("max-pull-bytes", "Maximum max bytes for a pull request to accept").PlaceHolder("BYTES").IntVar(&c.maxPullBytes)
		if !edit {
			f.Flag("pull", "Deliver messages in 'pull' mode").UnNegatableBoolVar(&c.pull)
			f.Flag("replay", "Replay Policy (instant, original)").PlaceHolder("POLICY").EnumVar(&c.replayPolicy, "instant", "original")
		}
		f.Flag("sample", "Percentage of requests to sample for monitoring purposes").Default("-1").IntVar(&c.samplePct)
		f.Flag("target", "Push based delivery target subject").PlaceHolder("SUBJECT").StringVar(&c.delivery)
		f.Flag("wait", "Acknowledgment waiting time").Default("-1s").DurationVar(&c.ackWait)
		if !edit {
			f.Flag("inactive-threshold", "How long to allow an ephemeral consumer to be idle before removing it").PlaceHolder("THRESHOLD").DurationVar(&c.inactiveThreshold)
			f.Flag("memory", "Force the consumer state to be stored in memory rather than inherit from the stream").UnNegatableBoolVar(&c.memory)
		}
		f.Flag("replicas", "Sets a custom replica count rather than inherit from the stream").IntVar(&c.replicas)
		f.Flag("metadata", "Adds metadata to the stream").PlaceHolder("META").IsSetByUser(&c.metadataIsSet).StringMapVar(&c.metadata)

	}

	cons := app.Command("consumer", "JetStream Consumer management").Alias("con").Alias("obs").Alias("c")
	addCheat("consumer", cons)
	cons.Flag("all", "Operate on all streams including system ones").Short('a').UnNegatableBoolVar(&c.showAll)

	consLs := cons.Command("ls", "List known Consumers").Alias("list").Action(c.lsAction)
	consLs.Arg("stream", "Stream name").StringVar(&c.stream)
	consLs.Flag("json", "Produce JSON output").Short('j').UnNegatableBoolVar(&c.json)
	consLs.Flag("names", "Show just the consumer names").Short('n').UnNegatableBoolVar(&c.listNames)

	conReport := cons.Command("report", "Reports on Consumer statistics").Action(c.reportAction)
	conReport.Arg("stream", "Stream name").StringVar(&c.stream)
	conReport.Flag("raw", "Show un-formatted numbers").Short('r').UnNegatableBoolVar(&c.raw)
	conReport.Flag("leaders", "Show details about the leaders").Short('l').UnNegatableBoolVar(&c.reportLeaderDistrib)

	consInfo := cons.Command("info", "Consumer information").Alias("nfo").Action(c.infoAction)
	consInfo.Arg("stream", "Stream name").StringVar(&c.stream)
	consInfo.Arg("consumer", "Consumer name").StringVar(&c.consumer)
	consInfo.Flag("json", "Produce JSON output").Short('j').UnNegatableBoolVar(&c.json)
	consInfo.Flag("no-select", "Do not select consumers from a list").Default("false").UnNegatableBoolVar(&c.force)

	consAdd := cons.Command("add", "Creates a new Consumer").Alias("create").Alias("new").Action(c.createAction)
	consAdd.Arg("stream", "Stream name").StringVar(&c.stream)
	consAdd.Arg("consumer", "Consumer name").StringVar(&c.consumer)
	consAdd.Flag("config", "JSON file to read configuration from").ExistingFileVar(&c.inputFile)
	consAdd.Flag("validate", "Only validates the configuration against the official Schema").UnNegatableBoolVar(&c.validateOnly)
	consAdd.Flag("output", "Save configuration instead of creating").PlaceHolder("FILE").StringVar(&c.outFile)
	addCreateFlags(consAdd, false)
	consAdd.Flag("defaults", "Accept default values for all prompts").UnNegatableBoolVar(&c.acceptDefaults)

	edit := cons.Command("edit", "Edits the configuration of a consumer").Alias("update").Action(c.editAction)
	edit.Arg("stream", "Stream name").StringVar(&c.stream)
	edit.Arg("consumer", "Consumer name").StringVar(&c.consumer)
	edit.Flag("config", "JSON file to read configuration from").ExistingFileVar(&c.inputFile)
	edit.Flag("force", "Force removal without prompting").Short('f').UnNegatableBoolVar(&c.force)
	edit.Flag("dry-run", "Only shows differences, do not edit the stream").UnNegatableBoolVar(&c.dryRun)
	addCreateFlags(edit, true)

	consRm := cons.Command("rm", "Removes a Consumer").Alias("delete").Alias("del").Action(c.rmAction)
	consRm.Arg("stream", "Stream name").StringVar(&c.stream)
	consRm.Arg("consumer", "Consumer name").StringVar(&c.consumer)
	consRm.Flag("force", "Force removal without prompting").Short('f').UnNegatableBoolVar(&c.force)

	consCp := cons.Command("copy", "Creates a new Consumer based on the configuration of another").Alias("cp").Action(c.cpAction)
	consCp.Arg("stream", "Stream name").Required().StringVar(&c.stream)
	consCp.Arg("source", "Source Consumer name").Required().StringVar(&c.consumer)
	consCp.Arg("destination", "Destination Consumer name").Required().StringVar(&c.destination)
	addCreateFlags(consCp, false)

	consNext := cons.Command("next", "Retrieves messages from Pull Consumers without interactive prompts").Action(c.nextAction)
	consNext.Arg("stream", "Stream name").Required().StringVar(&c.stream)
	consNext.Arg("consumer", "Consumer name").Required().StringVar(&c.consumer)
	consNext.Flag("ack", "Acknowledge received message").Default("true").IsSetByUser(&c.ackSetByUser).BoolVar(&c.ack)
	consNext.Flag("term", "Terms the message").Default("false").UnNegatableBoolVar(&c.term)
	consNext.Flag("raw", "Show only the message").Short('r').UnNegatableBoolVar(&c.raw)
	consNext.Flag("wait", "Wait up to this period to acknowledge messages").DurationVar(&c.ackWait)
	consNext.Flag("count", "Number of messages to try to fetch from the pull consumer").Default("1").IntVar(&c.pullCount)

	consSub := cons.Command("sub", "Retrieves messages from Consumers").Action(c.subAction)
	consSub.Arg("stream", "Stream name").StringVar(&c.stream)
	consSub.Arg("consumer", "Consumer name").StringVar(&c.consumer)
	consSub.Flag("ack", "Acknowledge received message").Default("true").BoolVar(&c.ack)
	consSub.Flag("raw", "Show only the message").Short('r').UnNegatableBoolVar(&c.raw)
	consSub.Flag("deliver-group", "Deliver group of the consumer").StringVar(&c.deliveryGroup)

	conCluster := cons.Command("cluster", "Manages a clustered Consumer").Alias("c")
	conClusterDown := conCluster.Command("step-down", "Force a new leader election by standing down the current leader").Alias("elect").Alias("down").Alias("d").Action(c.leaderStandDown)
	conClusterDown.Arg("stream", "Stream to act on").StringVar(&c.stream)
	conClusterDown.Arg("consumer", "Consumer to act on").StringVar(&c.consumer)
}

func init() {
	registerCommand("consumer", 4, configureConsumerCommand)
}

func (c *consumerCmd) leaderStandDown(_ *fisk.ParseContext) error {
	c.connectAndSetup(true, true)

	consumer, err := c.mgr.LoadConsumer(c.stream, c.consumer)
	if err != nil {
		return err
	}

	info, err := consumer.LatestState()
	if err != nil {
		return err
	}

	if info.Cluster == nil {
		return fmt.Errorf("consumer %q > %q is not clustered", consumer.StreamName(), consumer.Name())
	}

	leader := info.Cluster.Leader
	if leader == "" {
		return fmt.Errorf("consumer has no current leader")
	}

	log.Printf("Requesting leader step down of %q in a %d peer RAFT group", leader, len(info.Cluster.Replicas)+1)
	err = consumer.LeaderStepDown()
	if err != nil {
		return err
	}

	ctr := 0
	start := time.Now()
	for range time.NewTimer(500 * time.Millisecond).C {
		if ctr == 5 {
			return fmt.Errorf("consumer did not elect a new leader in time")
		}
		ctr++

		info, err = consumer.State()
		if err != nil {
			log.Printf("Failed to retrieve Consumer State: %s", err)
			continue
		}

		if info.Cluster.Leader != leader {
			log.Printf("New leader elected %q", info.Cluster.Leader)
			break
		}
	}

	if info.Cluster.Leader == leader {
		log.Printf("Leader did not change after %s", time.Since(start).Round(time.Millisecond))
	}

	fmt.Println()
	c.showConsumer(consumer)
	return nil
}

func (c *consumerCmd) editAction(pc *fisk.ParseContext) error {
	c.connectAndSetup(true, true)
	var err error

	if c.selectedConsumer == nil {
		c.selectedConsumer, err = c.mgr.LoadConsumer(c.stream, c.consumer)
		fisk.FatalIfError(err, "could not load Consumer")
	}

	if !c.selectedConsumer.IsDurable() {
		return fmt.Errorf("only durable consumers can be edited")
	}

	// lazy deep copy
	t := c.selectedConsumer.Configuration()
	tj, err := json.Marshal(t)
	if err != nil {
		return err
	}
	var ncfg api.ConsumerConfig

	if c.inputFile != "" {
		cf, err := os.ReadFile(c.inputFile)
		if err != nil {
			return err
		}
		err = json.Unmarshal(cf, &ncfg)
		if err != nil {
			return err
		}
	} else {
		err = json.Unmarshal(tj, &ncfg)
		if err != nil {
			return err
		}

		if c.description != "" {
			ncfg.Description = c.description
		}

		if c.maxDeliver != 0 {
			ncfg.MaxDeliver = c.maxDeliver
		}

		if c.maxAckPending != -1 {
			ncfg.MaxAckPending = c.maxAckPending
		}

		if c.ackWait != -1*time.Second {
			ncfg.AckWait = c.ackWait
		}

		if c.maxWaiting != 0 {
			ncfg.MaxWaiting = c.maxWaiting
		}

		if c.samplePct != -1 {
			ncfg.SampleFrequency = c.sampleFreqFromInt(c.samplePct)
		}

		if c.maxPullBatch > 0 {
			ncfg.MaxRequestBatch = c.maxPullBatch
		}

		if c.maxPullExpire > 0 {
			ncfg.MaxRequestExpires = c.maxPullExpire
		}

		if c.maxPullBytes > 0 {
			ncfg.MaxRequestMaxBytes = c.maxPullBytes
		}

		if c.backoffMode != "" {
			ncfg.BackOff, err = c.backoffPolicy()
			if err != nil {
				return fmt.Errorf("could not determine backoff policy: %v", err)
			}
		}

		if c.delivery != "" {
			ncfg.DeliverSubject = c.delivery
		}

		if c.hdrsOnlySet {
			ncfg.HeadersOnly = c.hdrsOnly
		}

		if len(c.filterSubjects) == 1 {
			ncfg.FilterSubject = c.filterSubjects[1]
		} else if len(c.filterSubjects) > 1 {
			ncfg.FilterSubjects = c.filterSubjects
		}

		if c.replicas > 0 {
			ncfg.Replicas = c.replicas
		}

		if c.metadataIsSet {
			ncfg.Metadata = c.metadata
		}
	}

	if len(ncfg.BackOff) > 0 && ncfg.AckWait != t.AckWait {
		return fmt.Errorf("consumers with backoff policies do not support editing Ack Wait")
	}

	// sort strings to subject lists that only differ in ordering is considered equal
	sorter := cmp.Transformer("Sort", func(in []string) []string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return out
	})

	diff := cmp.Diff(c.selectedConsumer.Configuration(), ncfg, sorter)
	if diff == "" {
		if !c.dryRun {
			fmt.Println("No difference in configuration")
		}

		return nil
	}

	fmt.Printf("Differences (-old +new):\n%s", diff)
	if c.dryRun {
		os.Exit(1)
	}

	if !c.force {
		ok, err := askConfirmation(fmt.Sprintf("Really edit Consumer %s > %s", c.stream, c.consumer), false)
		fisk.FatalIfError(err, "could not obtain confirmation")

		if !ok {
			return nil
		}
	}

	cons, err := c.mgr.NewConsumerFromDefault(c.stream, ncfg)
	if err != nil {
		return err
	}

	c.showConsumer(cons)

	return nil
}

func (c *consumerCmd) backoffPolicy() ([]time.Duration, error) {
	if c.backoffMode == "none" {
		return nil, nil
	}

	if c.backoffMode == "" || c.backoffSteps == 0 || c.backoffMin == 0 || c.backoffMax == 0 {
		return nil, fmt.Errorf("required policy properties not supplied")
	}

	switch c.backoffMode {
	case "linear":
		return jsm.LinearBackoffPeriods(c.backoffSteps, c.backoffMin, c.backoffMax)
	default:
		return nil, fmt.Errorf("invalid backoff mode %q", c.backoffMode)
	}
}

func (c *consumerCmd) rmAction(_ *fisk.ParseContext) error {
	var err error

	if c.force {
		if c.stream == "" || c.consumer == "" {
			return fmt.Errorf("--force requires a stream and consumer name")
		}

		c.nc, c.mgr, err = prepareHelper("", natsOpts()...)
		fisk.FatalIfError(err, "setup failed")

		err = c.mgr.DeleteConsumer(c.stream, c.consumer)
		if err != nil {
			if err == context.DeadlineExceeded {
				fmt.Println("Delete failed due to timeout, the stream or consumer might not exist or be in an unmanageable state")
			}
		}

		return err
	}

	c.connectAndSetup(true, true)

	ok, err := askConfirmation(fmt.Sprintf("Really delete Consumer %s > %s", c.stream, c.consumer), false)
	fisk.FatalIfError(err, "could not obtain confirmation")

	if !ok {
		return nil
	}

	if c.selectedConsumer == nil {
		c.selectedConsumer, err = c.mgr.LoadConsumer(c.stream, c.consumer)
		fisk.FatalIfError(err, "could not load Consumer")
	}

	return c.selectedConsumer.Delete()
}

func (c *consumerCmd) lsAction(pc *fisk.ParseContext) error {
	c.connectAndSetup(true, false)

	stream, err := c.mgr.LoadStream(c.stream)
	fisk.FatalIfError(err, "could not load Consumers")

	consumers, err := stream.ConsumerNames()
	fisk.FatalIfError(err, "could not load Consumers")

	if c.json {
		err = printJSON(consumers)
		fisk.FatalIfError(err, "could not display Consumers")
		return nil
	}

	if c.listNames {
		for _, sc := range consumers {
			fmt.Println(sc)
		}

		return nil
	}

	if len(consumers) == 0 {
		fmt.Println("No Consumers defined")
		return nil
	}

	fmt.Printf("Consumers for Stream %s:\n", c.stream)
	fmt.Println()
	for _, sc := range consumers {
		fmt.Printf("\t%s\n", sc)
	}
	fmt.Println()

	return nil
}

func (c *consumerCmd) showConsumer(consumer *jsm.Consumer) {
	config := consumer.Configuration()
	state, err := consumer.LatestState()
	fisk.FatalIfError(err, "could not load Consumer %s > %s", c.stream, c.consumer)

	c.showInfo(config, state)
}

func (c *consumerCmd) renderBackoff(bo []time.Duration) string {
	if len(bo) == 0 {
		return "unset"
	}

	var times []string

	if len(bo) > 15 {
		for _, d := range bo[:5] {
			times = append(times, d.String())
		}
		times = append(times, "...")
		for _, d := range bo[len(bo)-5:] {
			times = append(times, d.String())
		}

		return fmt.Sprintf("%s (%d total)", strings.Join(times, ", "), len(bo))
	} else {
		for _, p := range bo {
			times = append(times, p.String())
		}

		return strings.Join(times, ", ")
	}
}

func (c *consumerCmd) showInfo(config api.ConsumerConfig, state api.ConsumerInfo) {
	if c.json {
		printJSON(state)
		return
	}

	fmt.Printf("Information for Consumer %s > %s created %s\n", state.Stream, state.Name, state.Created.Local().Format(time.RFC3339))
	fmt.Println()
	fmt.Println("Configuration:")
	fmt.Println()
	if config.Name != "" {
		fmt.Printf("                Name: %s\n", config.Name)
	}
	if config.Durable != "" && config.Durable != config.Name {
		fmt.Printf("        Durable Name: %s\n", config.Durable)
	}
	if config.Description != "" {
		fmt.Printf("         Description: %s\n", config.Description)
	}
	if config.DeliverSubject != "" {
		fmt.Printf("    Delivery Subject: %s\n", config.DeliverSubject)
	} else {
		fmt.Printf("           Pull Mode: true\n")
	}
	if config.FilterSubject != "" {
		fmt.Printf("      Filter Subject: %s\n", config.FilterSubject)
	} else if len(config.FilterSubjects) > 0 {
		fmt.Printf("     Filter Subjects: %s\n", strings.Join(config.FilterSubjects, ", "))
	}
	switch config.DeliverPolicy {
	case api.DeliverAll:
		fmt.Printf("      Deliver Policy: All\n")
	case api.DeliverLast:
		fmt.Printf("      Deliver Policy: Last\n")
	case api.DeliverNew:
		fmt.Printf("      Deliver Policy: New\n")
	case api.DeliverLastPerSubject:
		fmt.Printf("      Deliver Policy: Last Per Subject\n")
	case api.DeliverByStartTime:
		fmt.Printf("      Deliver Policy: Since %v\n", config.OptStartTime)
	case api.DeliverByStartSequence:
		fmt.Printf("      Deliver Policy: From Sequence %d\n", config.OptStartSeq)
	}
	if config.DeliverGroup != "" && config.DeliverSubject != "" {
		fmt.Printf(" Deliver Queue Group: %s\n", config.DeliverGroup)
	}
	fmt.Printf("          Ack Policy: %s\n", config.AckPolicy.String())
	if config.AckPolicy != api.AckNone {
		fmt.Printf("            Ack Wait: %v\n", config.AckWait)
	}
	fmt.Printf("       Replay Policy: %s\n", config.ReplayPolicy.String())
	if config.MaxDeliver != -1 {
		fmt.Printf("  Maximum Deliveries: %d\n", config.MaxDeliver)
	}
	if config.SampleFrequency != "" {
		fmt.Printf("       Sampling Rate: %s\n", config.SampleFrequency)
	}
	if config.RateLimit > 0 {
		fmt.Printf("          Rate Limit: %s / second\n", humanize.IBytes(config.RateLimit/8))
	}
	if config.MaxAckPending > 0 {
		fmt.Printf("     Max Ack Pending: %s\n", humanize.Comma(int64(config.MaxAckPending)))
	}
	if config.MaxWaiting > 0 {
		fmt.Printf("   Max Waiting Pulls: %s\n", humanize.Comma(int64(config.MaxWaiting)))
	}
	if config.Heartbeat > 0 {
		fmt.Printf("      Idle Heartbeat: %s\n", humanizeDuration(config.Heartbeat))
	}
	if config.DeliverSubject != "" {
		fmt.Printf("        Flow Control: %v\n", config.FlowControl)
	}
	if config.HeadersOnly {
		fmt.Printf("        Headers Only: true\n")
	}
	if config.InactiveThreshold > 0 && config.DeliverSubject == "" {
		fmt.Printf("  Inactive Threshold: %s\n", humanizeDuration(config.InactiveThreshold))
	}
	if config.MaxRequestExpires > 0 {
		fmt.Printf("     Max Pull Expire: %s\n", humanizeDuration(config.MaxRequestExpires))
	}
	if config.MaxRequestBatch > 0 {
		fmt.Printf("      Max Pull Batch: %s\n", humanize.Comma(int64(config.MaxRequestBatch)))
	}
	if config.MaxRequestMaxBytes > 0 {
		fmt.Printf("   Max Pull MaxBytes: %s\n", humanize.Comma(int64(config.MaxRequestMaxBytes)))
	}
	if len(config.BackOff) > 0 {
		fmt.Printf("             Backoff: %s\n", c.renderBackoff(config.BackOff))
	}
	if config.Replicas > 0 {
		fmt.Printf("            Replicas: %d\n", config.Replicas)
	}
	if config.MemoryStorage {
		fmt.Printf("      Memory Storage: yes\n")
	}
	fmt.Println()

	if len(config.Metadata) > 0 {
		fmt.Println("Metadata:")
		fmt.Println()
		dumpMapStrings(config.Metadata, 3)
		fmt.Println()
	}

	if state.Cluster != nil && state.Cluster.Name != "" {
		fmt.Println("Cluster Information:")
		fmt.Println()
		fmt.Printf("                Name: %s\n", state.Cluster.Name)
		fmt.Printf("              Leader: %s\n", state.Cluster.Leader)
		for _, r := range state.Cluster.Replicas {
			since := fmt.Sprintf("seen %s ago", humanizeDuration(r.Active))
			if r.Active == 0 || r.Active == math.MaxInt64 {
				since = "not seen"
			}

			if r.Current {
				fmt.Printf("             Replica: %s, current, %s\n", r.Name, since)
			} else {
				fmt.Printf("             Replica: %s, outdated, %s\n", r.Name, since)
			}
		}
		fmt.Println()
	}

	fmt.Println("State:")
	fmt.Println()
	if state.Delivered.Last == nil {
		fmt.Printf("   Last Delivered Message: Consumer sequence: %s Stream sequence: %s\n", humanize.Comma(int64(state.Delivered.Consumer)), humanize.Comma(int64(state.Delivered.Stream)))
	} else {
		fmt.Printf("   Last Delivered Message: Consumer sequence: %s Stream sequence: %s Last delivery: %s ago\n", humanize.Comma(int64(state.Delivered.Consumer)), humanize.Comma(int64(state.Delivered.Stream)), humanizeDuration(time.Since(*state.Delivered.Last)))
	}

	if config.AckPolicy != api.AckNone {
		if state.AckFloor.Last == nil {
			fmt.Printf("     Acknowledgment floor: Consumer sequence: %s Stream sequence: %s\n", humanize.Comma(int64(state.AckFloor.Consumer)), humanize.Comma(int64(state.AckFloor.Stream)))
		} else {
			fmt.Printf("     Acknowledgment floor: Consumer sequence: %s Stream sequence: %s Last Ack: %s ago\n", humanize.Comma(int64(state.AckFloor.Consumer)), humanize.Comma(int64(state.AckFloor.Stream)), humanizeDuration(time.Since(*state.AckFloor.Last)))
		}
		if config.MaxAckPending > 0 {
			fmt.Printf("         Outstanding Acks: %s out of maximum %s\n", humanize.Comma(int64(state.NumAckPending)), humanize.Comma(int64(config.MaxAckPending)))
		} else {
			fmt.Printf("         Outstanding Acks: %s\n", humanize.Comma(int64(state.NumAckPending)))
		}
		fmt.Printf("     Redelivered Messages: %s\n", humanize.Comma(int64(state.NumRedelivered)))
	}

	fmt.Printf("     Unprocessed Messages: %s\n", humanize.Comma(int64(state.NumPending)))
	if config.DeliverSubject == "" {
		if config.MaxWaiting > 0 {
			fmt.Printf("            Waiting Pulls: %s of maximum %s\n", humanize.Comma(int64(state.NumWaiting)), humanize.Comma(int64(config.MaxWaiting)))
		} else {
			fmt.Printf("            Waiting Pulls: %s of unlimited\n", humanize.Comma(int64(state.NumWaiting)))
		}
	} else {
		if state.PushBound {
			if config.DeliverGroup != "" {
				fmt.Printf("          Active Interest: Active using Queue Group %s", config.DeliverGroup)
			} else {
				fmt.Printf("          Active Interest: Active")
			}
		} else {
			fmt.Printf("          Active Interest: No interest")
		}
	}

	fmt.Println()
}

func (c *consumerCmd) infoAction(_ *fisk.ParseContext) error {
	c.connectAndSetup(true, true)

	var err error
	consumer := c.selectedConsumer
	if consumer == nil {
		consumer, err = c.mgr.LoadConsumer(c.stream, c.consumer)
		fisk.FatalIfError(err, "could not load Consumer %s > %s", c.stream, c.consumer)
	}

	c.showConsumer(consumer)

	return nil
}

func (c *consumerCmd) replayPolicyFromString(p string) api.ReplayPolicy {
	switch strings.ToLower(p) {
	case "instant":
		return api.ReplayInstant
	case "original":
		return api.ReplayOriginal
	default:
		fisk.Fatalf("invalid replay policy '%s'", p)
		return api.ReplayInstant
	}
}

func (c *consumerCmd) ackPolicyFromString(p string) api.AckPolicy {
	switch strings.ToLower(p) {
	case "none":
		return api.AckNone
	case "all":
		return api.AckAll
	case "explicit":
		return api.AckExplicit
	default:
		fisk.Fatalf("invalid ack policy '%s'", p)
		// unreachable
		return api.AckExplicit
	}
}

func (c *consumerCmd) sampleFreqFromInt(s int) string {
	if s > 100 || s < 0 {
		fisk.Fatalf("sample percent is not between 0 and 100")
	}

	if s > 0 {
		return strconv.Itoa(c.samplePct)
	}

	return ""
}

func (c *consumerCmd) defaultConsumer() *api.ConsumerConfig {
	return &api.ConsumerConfig{
		AckPolicy:    api.AckExplicit,
		ReplayPolicy: api.ReplayInstant,
	}
}

func (c *consumerCmd) setStartPolicy(cfg *api.ConsumerConfig, policy string) {
	if policy == "" {
		return
	}

	if policy == "all" {
		cfg.DeliverPolicy = api.DeliverAll
	} else if policy == "last" {
		cfg.DeliverPolicy = api.DeliverLast
	} else if policy == "new" || policy == "next" {
		cfg.DeliverPolicy = api.DeliverNew
	} else if policy == "subject" || policy == "last_per_subject" {
		cfg.DeliverPolicy = api.DeliverLastPerSubject
	} else if ok, _ := regexp.MatchString("^\\d+$", policy); ok {
		seq, _ := strconv.Atoi(policy)
		cfg.DeliverPolicy = api.DeliverByStartSequence
		cfg.OptStartSeq = uint64(seq)
	} else {
		d, err := parseDurationString(policy)
		fisk.FatalIfError(err, "could not parse starting delta")
		t := time.Now().UTC().Add(-d)
		cfg.DeliverPolicy = api.DeliverByStartTime
		cfg.OptStartTime = &t
	}
}

func (c *consumerCmd) cpAction(pc *fisk.ParseContext) (err error) {
	c.connectAndSetup(true, false)

	source, err := c.mgr.LoadConsumer(c.stream, c.consumer)
	fisk.FatalIfError(err, "could not load source Consumer")

	cfg := source.Configuration()

	if c.ackWait > 0 {
		cfg.AckWait = c.ackWait
	}

	if c.samplePct != -1 {
		cfg.SampleFrequency = c.sampleFreqFromInt(c.samplePct)
	}

	if c.startPolicy != "" {
		c.setStartPolicy(&cfg, c.startPolicy)
	}

	if c.ephemeral {
		cfg.Durable = ""
	} else {
		cfg.Durable = c.destination
	}

	if c.delivery != "" {
		cfg.DeliverSubject = c.delivery
	}

	if c.pull {
		cfg.DeliverSubject = ""
		cfg.MaxWaiting = c.maxWaiting
	}

	if c.ackPolicy != "" {
		cfg.AckPolicy = c.ackPolicyFromString(c.ackPolicy)
	}

	if len(c.filterSubjects) == 1 {
		cfg.FilterSubject = c.filterSubjects[0]
	} else if len(c.filterSubjects) > 1 {
		cfg.FilterSubjects = c.filterSubjects
	}

	if c.replayPolicy != "" {
		cfg.ReplayPolicy = c.replayPolicyFromString(c.replayPolicy)
	}

	if c.maxDeliver != 0 {
		cfg.MaxDeliver = c.maxDeliver
	}

	if c.bpsRateLimit != 0 {
		cfg.RateLimit = c.bpsRateLimit
	}

	if c.maxAckPending != -1 {
		cfg.MaxAckPending = c.maxAckPending
	}

	if c.idleHeartbeat != "" && c.idleHeartbeat != "-" {
		hb, err := parseDurationString(c.idleHeartbeat)
		fisk.FatalIfError(err, "Invalid heartbeat duration")
		cfg.Heartbeat = hb
	}

	if c.description != "" {
		cfg.Description = c.description
	}

	if c.fcSet {
		cfg.FlowControl = c.fc
	}

	if cfg.DeliverSubject == "" {
		cfg.Heartbeat = 0
		cfg.FlowControl = false
	}

	if c.deliveryGroup != "_unset_" {
		cfg.DeliverGroup = c.deliveryGroup
	}

	if c.delivery == "" {
		cfg.DeliverGroup = ""
	}

	if c.deliveryGroup == "_unset_" {
		cfg.DeliverGroup = ""
	}

	if c.inactiveThreshold > 0 {
		cfg.InactiveThreshold = c.inactiveThreshold
	}

	if c.maxPullExpire > 0 {
		cfg.MaxRequestExpires = c.maxPullExpire
	}

	if c.maxPullBatch > 0 {
		cfg.MaxRequestBatch = c.maxPullBatch
	}

	if c.maxPullBytes > 0 {
		cfg.MaxRequestMaxBytes = c.maxPullBytes
	}

	if c.backoffMode != "" {
		cfg.BackOff, err = c.backoffPolicy()
		if err != nil {
			return fmt.Errorf("could not determine backoff policy: %v", err)
		}
	}

	if c.hdrsOnlySet {
		cfg.HeadersOnly = c.hdrsOnly
	}

	consumer, err := c.mgr.NewConsumerFromDefault(c.stream, cfg)
	fisk.FatalIfError(err, "Consumer creation failed")

	if cfg.Durable == "" {
		return nil
	}

	c.consumer = cfg.Durable

	c.showConsumer(consumer)

	return nil
}

func (c *consumerCmd) loadConfigFile(file string) (*api.ConsumerConfig, error) {
	f, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	var cfg api.ConsumerConfig

	// there is a chance that this is a `nats c info --json` output
	// which is a ConsumerInfo, so we detect if this is one of those
	// by checking if there's a config key then extract that, else
	// we try loading it as a StreamConfig

	var nfo map[string]any
	err = json.Unmarshal(f, &nfo)
	if err != nil {
		return nil, err
	}

	_, ok := nfo["config"]
	if ok {
		var nfo api.ConsumerInfo
		err = json.Unmarshal(f, &nfo)
		if err != nil {
			return nil, err
		}
		cfg = nfo.Config
	} else {
		err = json.Unmarshal(f, &cfg)
		if err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}

func (c *consumerCmd) prepareConfig(pc *fisk.ParseContext) (cfg *api.ConsumerConfig, err error) {
	cfg = c.defaultConsumer()
	cfg.Description = c.description

	if c.inputFile != "" {
		cfg, err = c.loadConfigFile(c.inputFile)
		if err != nil {
			return nil, err
		}

		if cfg.Durable != "" && c.consumer != "" && cfg.Durable != c.consumer {
			if c.consumer != "" {
				cfg.Durable = c.consumer
			} else {
				return cfg, fmt.Errorf("durable consumer name in %s does not match CLI consumer name %s", c.inputFile, c.consumer)
			}
		}

		if cfg.DeliverSubject != "" && c.delivery != "" {
			cfg.DeliverSubject = c.delivery
		}

		return cfg, err
	}

	if c.consumer == "" && !c.ephemeral {
		err = askOne(&survey.Input{
			Message: "Consumer name",
			Help:    "This will be used for the name of the durable subscription to be used when referencing this Consumer later. Settable using 'name' CLI argument",
		}, &c.consumer, survey.WithValidator(survey.Required))
		fisk.FatalIfError(err, "could not request durable name")
	}
	cfg.Durable = c.consumer

	if ok, _ := regexp.MatchString(`\.|\*|>`, cfg.Durable); ok {
		fisk.Fatalf("durable name can not contain '.', '*', '>'")
	}

	if !c.pull && c.delivery == "" {
		err = askOne(&survey.Input{
			Message: "Delivery target (empty for Pull Consumers)",
			Help:    "Consumers can be in 'push' or 'pull' mode, in 'push' mode messages are dispatched in real time to a target NATS subject, this is that subject. Leaving this blank creates a 'pull' mode Consumer. Settable using --target and --pull",
		}, &c.delivery)
		fisk.FatalIfError(err, "could not request delivery target")
	}

	cfg.DeliverSubject = c.delivery

	if c.acceptDefaults {
		if c.deliveryGroup == "_unset_" {
			c.deliveryGroup = ""
		}
		if c.startPolicy == "" {
			c.startPolicy = "all"
		}
		if c.ackPolicy == "" {
			c.ackPolicy = "none"
			if c.pull || c.delivery == "" {
				c.ackPolicy = "explicit"
			}
		}
		if c.maxDeliver == 0 {
			c.maxDeliver = -1
		}
		if c.maxAckPending == -1 {
			c.maxAckPending = 0
		}
		if c.replayPolicy == "" {
			c.replayPolicy = "instant"
		}
		if c.idleHeartbeat == "" {
			c.idleHeartbeat = "-1"
		}
		if !c.hdrsOnlySet {
			c.hdrsOnlySet = true
		}
		if cfg.DeliverSubject != "" {
			c.replayPolicy = "instant"
			c.fcSet = true
		}
	}

	if cfg.DeliverSubject != "" && c.deliveryGroup == "_unset_" {
		err = askOne(&survey.Input{
			Message: "Delivery Queue Group",
			Help:    "When set push consumers will only deliver messages to subscriptions matching this queue group",
		}, &c.deliveryGroup)
		fisk.FatalIfError(err, "could not request delivery group")
	}
	cfg.DeliverGroup = c.deliveryGroup
	if cfg.DeliverGroup == "_unset_" {
		cfg.DeliverGroup = ""
	}

	if c.startPolicy == "" {
		err = askOne(&survey.Input{
			Message: "Start policy (all, new, last, subject, 1h, msg sequence)",
			Help:    "This controls how the Consumer starts out, does it make all messages available, only the latest, latest per subject, ones after a certain time or time sequence. Settable using --deliver",
			Default: "all",
		}, &c.startPolicy, survey.WithValidator(survey.Required))
		fisk.FatalIfError(err, "could not request start policy")
	}

	c.setStartPolicy(cfg, c.startPolicy)

	if c.ackPolicy == "" {
		valid := []string{"explicit", "all", "none"}
		dflt := "none"
		if c.delivery == "" {
			dflt = "explicit"
		}

		err = askOne(&survey.Select{
			Message: "Acknowledgment policy",
			Options: valid,
			Default: dflt,
			Help:    "Messages that are not acknowledged will be redelivered at a later time. 'none' means no acknowledgement is needed only 1 delivery ever, 'all' means acknowledging message 10 will also acknowledge 0-9 and 'explicit' means each has to be acknowledged specifically. Settable using --ack",
		}, &c.ackPolicy)
		fisk.FatalIfError(err, "could not ask acknowledgement policy")
	}

	if c.replayPolicy == "" {
		err = askOne(&survey.Select{
			Message: "Replay policy",
			Options: []string{"instant", "original"},
			Default: "instant",
			Help:    "Messages can be replayed at the rate they arrived in or as fast as possible. Settable using --replay",
		}, &c.replayPolicy)
		fisk.FatalIfError(err, "could not ask replay policy")
	}

	cfg.AckPolicy = c.ackPolicyFromString(c.ackPolicy)
	if cfg.AckPolicy == api.AckNone && cfg.DeliverSubject == "" {
		fisk.Fatalf("pull consumers can only be explicit or all acknowledgement modes")
	}

	if cfg.AckPolicy == api.AckNone {
		cfg.MaxDeliver = -1
	}

	if c.ackWait > 0 {
		cfg.AckWait = c.ackWait
	}

	if c.samplePct > 0 {
		if c.samplePct > 100 {
			fisk.Fatalf("sample percent is not between 0 and 100")
		}

		cfg.SampleFrequency = strconv.Itoa(c.samplePct)
	}

	if cfg.DeliverSubject != "" {
		if c.replayPolicy == "" {
			mode := ""
			err = askOne(&survey.Select{
				Message: "Replay policy",
				Options: []string{"instant", "original"},
				Default: "instant",
				Help:    "Replay policy is the time interval at which messages are delivered to interested parties. 'instant' means deliver all as soon as possible while 'original' will match the time intervals in which messages were received, useful for replaying production traffic in development. Settable using --replay",
			}, &mode)
			fisk.FatalIfError(err, "could not ask replay policy")
			c.replayPolicy = mode
		}
	}

	if c.replayPolicy != "" {
		cfg.ReplayPolicy = c.replayPolicyFromString(c.replayPolicy)
	}

	switch {
	case len(c.filterSubjects) == 0 && !c.acceptDefaults:
		sub := ""
		err = askOne(&survey.Input{
			Message: "Filter Stream by subject (blank for all)",
			Default: "",
			Help:    "Stream can consume more than one subject - or a wildcard - this allows you to filter out just a single subject from all the ones entering the Stream for delivery to the Consumer. Settable using --filter",
		}, &sub)
		fisk.FatalIfError(err, "could not ask for filtering subject")
		c.filterSubjects = []string{sub}
	}

	switch {
	case len(c.filterSubjects) == 1:
		cfg.FilterSubject = c.filterSubjects[0]
	case len(c.filterSubjects) > 1:
		cfg.FilterSubjects = c.filterSubjects
	}

	if cfg.FilterSubject == "" && len(c.filterSubjects) == 0 && cfg.DeliverPolicy == api.DeliverLastPerSubject {
		cfg.FilterSubject = ">"
	}

	if c.maxDeliver == 0 && cfg.AckPolicy != api.AckNone {
		err = askOne(&survey.Input{
			Message: "Maximum Allowed Deliveries",
			Default: "-1",
			Help:    "When this is -1 unlimited attempts to deliver an un acknowledged message is made, when this is >0 it will be maximum amount of times a message is delivered after which it is ignored. Settable using --max-deliver.",
		}, &c.maxDeliver)
		fisk.FatalIfError(err, "could not ask for maximum allowed deliveries")
	}

	if c.maxAckPending == -1 && cfg.AckPolicy != api.AckNone {
		err = askOne(&survey.Input{
			Message: "Maximum Acknowledgments Pending",
			Default: "0",
			Help:    "The maximum number of messages without acknowledgement that can be outstanding, once this limit is reached message delivery will be suspended. Settable using --max-pending.",
		}, &c.maxAckPending)
		fisk.FatalIfError(err, "could not ask for maximum outstanding acknowledgements")
	}

	if cfg.DeliverSubject != "" {
		if c.idleHeartbeat == "-1" {
			cfg.Heartbeat = 0
		} else if c.idleHeartbeat != "" {
			cfg.Heartbeat, err = parseDurationString(c.idleHeartbeat)
			fisk.FatalIfError(err, "invalid heartbeat duration")
		} else {
			idle := "0s"
			err = askOne(&survey.Input{
				Message: "Idle Heartbeat",
				Help:    "When a Push consumer is idle for the given period an empty message with a Status header of 100 will be sent to the delivery subject, settable using --heartbeat",
				Default: "0s",
			}, &idle)
			fisk.FatalIfError(err, "could not ask for idle heartbeat")
			cfg.Heartbeat, err = parseDurationString(idle)
			fisk.FatalIfError(err, "invalid heartbeat duration")
		}
	}

	if cfg.DeliverSubject != "" {
		if !c.fcSet {
			c.fc, err = askConfirmation("Enable Flow Control, ie --flow-control", false)
			fisk.FatalIfError(err, "could not ask flow control")
		}

		cfg.FlowControl = c.fc
	}

	if !c.hdrsOnlySet {
		c.hdrsOnly, err = askConfirmation("Deliver headers only without bodies", false)
		fisk.FatalIfError(err, "could not ask headers only")
	}
	cfg.HeadersOnly = c.hdrsOnly

	if !c.acceptDefaults && c.backoffMode == "" {
		err = c.askBackoffPolicy()
		if err != nil {
			return nil, err
		}
	}

	if c.backoffMode != "" {
		cfg.BackOff, err = c.backoffPolicy()
		if err != nil {
			return nil, fmt.Errorf("could not determine backoff policy: %v", err)
		}

		// hopefully this is just to work around a temporary bug in the server
		if c.maxDeliver == -1 && len(cfg.BackOff) > 0 {
			c.maxDeliver = len(cfg.BackOff) + 1
		}
	}

	if c.maxAckPending == -1 {
		c.maxAckPending = 0
	}

	cfg.MaxAckPending = c.maxAckPending
	cfg.InactiveThreshold = c.inactiveThreshold

	if c.maxPullBatch > 0 {
		cfg.MaxRequestBatch = c.maxPullBatch
	}

	if c.maxPullExpire > 0 {
		cfg.MaxRequestExpires = c.maxPullExpire
	}

	if c.maxPullBytes > 0 {
		cfg.MaxRequestMaxBytes = c.maxPullBytes
	}

	if cfg.DeliverSubject == "" {
		cfg.MaxWaiting = c.maxWaiting
	}

	if c.maxDeliver != 0 && cfg.AckPolicy != api.AckNone {
		cfg.MaxDeliver = c.maxDeliver
	}

	if c.bpsRateLimit > 0 && cfg.DeliverSubject == "" {
		return nil, fmt.Errorf("rate limits are only possible on Push consumers")
	}

	cfg.RateLimit = c.bpsRateLimit
	cfg.Replicas = c.replicas
	cfg.MemoryStorage = c.memory

	if c.metadataIsSet {
		cfg.Metadata = c.metadata
	}

	return cfg, nil
}

func (c *consumerCmd) askBackoffPolicy() error {
	ok, err := askConfirmation("Add a Retry Backoff Policy", false)
	if err != nil {
		return err
	}

	if ok {
		err = askOne(&survey.Select{
			Message: "Backoff policy",
			Options: []string{"linear", "none"},
			Default: "none",
			Help:    "Adds a Backoff policy for use with delivery retries. Linear grows at equal intervals between min and max.",
		}, &c.backoffMode)
		if err != nil {
			return err
		}

		if c.backoffMode == "none" {
			return nil
		}

		d := ""
		err := askOne(&survey.Input{
			Message: "Minimum retry time",
			Help:    "Backoff policies range from min to max",
			Default: "1m",
		}, &d)
		if err != nil {
			return err
		}
		c.backoffMin, err = parseDurationString(d)
		if err != nil {
			return err
		}

		err = askOne(&survey.Input{
			Message: "Maximum retry time",
			Help:    "Backoff policies range from min to max",
			Default: "10m",
		}, &d)
		if err != nil {
			return err
		}
		c.backoffMax, err = parseDurationString(d)
		if err != nil {
			return err
		}

		steps, err := askOneInt("Number of steps to generate in the policy", "20", "Number of steps to create between min and max")
		if err != nil {
			return err
		}
		if steps < 1 {
			return fmt.Errorf("backoff steps must be > 0")
		}
		c.backoffSteps = uint(steps)
	}

	return nil
}

func (c *consumerCmd) validateCfg(cfg *api.ConsumerConfig) (bool, []byte, []string, error) {
	j, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, nil, nil, err
	}

	if os.Getenv("NOVALIDATE") != "" {
		return true, nil, nil, nil
	}

	valid, errs := cfg.Validate(new(SchemaValidator))

	return valid, j, errs, nil
}

func (c *consumerCmd) createAction(pc *fisk.ParseContext) (err error) {
	cfg, err := c.prepareConfig(pc)
	if err != nil {
		return err
	}

	switch {
	case c.validateOnly:
		valid, j, errs, err := c.validateCfg(cfg)
		fisk.FatalIfError(err, "Could not validate configuration")

		fmt.Println(string(j))
		fmt.Println()
		if !valid {
			fisk.Fatalf("Validation Failed: %s", strings.Join(errs, "\n\t"))
		}

		fmt.Println("Configuration is a valid Consumer")
		return nil

	case c.outFile != "":
		valid, j, errs, err := c.validateCfg(cfg)
		fisk.FatalIfError(err, "Could not validate configuration")

		if !valid {
			fisk.Fatalf("Validation Failed: %s", strings.Join(errs, "\n\t"))
		}

		return os.WriteFile(c.outFile, j, 0644)
	}

	c.connectAndSetup(true, false)

	created, err := c.mgr.NewConsumerFromDefault(c.stream, *cfg)
	fisk.FatalIfError(err, "Consumer creation failed")

	c.consumer = created.Name()

	c.showConsumer(created)

	return nil
}

func (c *consumerCmd) getNextMsgDirect(stream string, consumer string) error {
	req := &api.JSApiConsumerGetNextRequest{Batch: 1, Expires: opts.Timeout}

	sub, err := c.nc.SubscribeSync(c.nc.NewRespInbox())
	fisk.FatalIfError(err, "subscribe failed")
	sub.AutoUnsubscribe(1)

	err = c.mgr.NextMsgRequest(stream, consumer, sub.Subject, req)
	fisk.FatalIfError(err, "could not request next message")

	fatalIfNotPull := func() {
		cons, err := c.mgr.LoadConsumer(stream, consumer)
		fisk.FatalIfError(err, "could not load consumer %q", consumer)

		if !cons.IsPullMode() {
			fisk.Fatalf("consumer %q is not a Pull consumer", consumer)
		}
	}

	if c.term {
		if !c.ackSetByUser {
			c.ack = false
		}

		if c.ack {
			fisk.Fatalf("can not both Acknowledge and Terminate message")
		}
	}

	msg, err := sub.NextMsg(opts.Timeout)
	if err != nil {
		fatalIfNotPull()
	}
	fisk.FatalIfError(err, "no message received")

	if msg.Header != nil && msg.Header.Get("Status") == "503" {
		fatalIfNotPull()
	}

	if !c.raw {
		info, err := jsm.ParseJSMsgMetadata(msg)
		if err != nil {
			if msg.Reply == "" {
				fmt.Printf("--- subject: %s\n", msg.Subject)
			} else {
				fmt.Printf("--- subject: %s reply: %s\n", msg.Subject, msg.Reply)
			}

		} else {
			fmt.Printf("[%s] subj: %s / tries: %d / cons seq: %d / str seq: %d / pending: %s\n", time.Now().Format("15:04:05"), msg.Subject, info.Delivered(), info.ConsumerSequence(), info.StreamSequence(), humanize.Comma(int64(info.Pending())))
		}

		if len(msg.Header) > 0 {
			fmt.Println()
			fmt.Println("Headers:")
			fmt.Println()
			for h, vals := range msg.Header {
				for _, val := range vals {
					fmt.Printf("  %s: %s\n", h, val)
				}
			}

			fmt.Println()
			fmt.Println("Data:")
			fmt.Println()
		}

		fmt.Println()
		fmt.Println(string(msg.Data))
	} else {
		fmt.Println(string(msg.Data))
	}

	if c.term {
		err = msg.Term()
		fisk.FatalIfError(err, "could not Terminate message")
		c.nc.Flush()
		fmt.Println("\nTerminated message")
	}

	if c.ack {
		var stime time.Duration
		if c.ackWait > 0 {
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			stime = time.Duration(r.Intn(int(c.ackWait)))
		}

		if stime > 0 {
			time.Sleep(stime)
		}

		err = msg.Respond(nil)
		fisk.FatalIfError(err, "could not Acknowledge message")
		c.nc.Flush()
		if !c.raw {
			if stime > 0 {
				fmt.Printf("\nAcknowledged message after %s delay\n", stime)
			} else {
				fmt.Println("\nAcknowledged message")
			}
			fmt.Println()
		}
	}

	return nil
}

func (c *consumerCmd) subscribeConsumer(consumer *jsm.Consumer) (err error) {
	if !c.raw {
		fmt.Printf("Subscribing to topic %s auto acknowlegement: %v\n\n", consumer.DeliverySubject(), c.ack)
		fmt.Println("Consumer Info:")
		fmt.Printf("  Ack Policy: %s\n", consumer.AckPolicy().String())
		if consumer.AckPolicy() != api.AckNone {
			fmt.Printf("    Ack Wait: %v\n", consumer.AckWait())
		}
		fmt.Println()
	}

	handler := func(m *nats.Msg) {
		if len(m.Data) == 0 && m.Header.Get("Status") == "100" {
			stalled := m.Header.Get("Nats-Consumer-Stalled")
			if stalled != "" {
				c.nc.Publish(stalled, nil)
			} else {
				m.Respond(nil)
			}

			return
		}

		var msginfo *jsm.MsgInfo
		var err error

		if len(m.Reply) > 0 {
			msginfo, err = jsm.ParseJSMsgMetadata(m)
		}

		fisk.FatalIfError(err, "could not parse JetStream metadata: '%s'", m.Reply)

		if !c.raw {
			now := time.Now().Format("15:04:05")

			if msginfo != nil {
				fmt.Printf("[%s] subj: %s / tries: %d / cons seq: %d / str seq: %d / pending: %s\n", now, m.Subject, msginfo.Delivered(), msginfo.ConsumerSequence(), msginfo.StreamSequence(), humanize.Comma(int64(msginfo.Pending())))
			} else {
				fmt.Printf("[%s] %s reply: %s\n", now, m.Subject, m.Reply)
			}

			if len(m.Header) > 0 {
				if len(m.Data) == 0 && m.Reply != "" && m.Header.Get("Status") == "100" {
					m.Respond(nil)
					return
				}

				fmt.Println()
				fmt.Println("Headers:")
				fmt.Println()

				for h, vals := range m.Header {
					for _, val := range vals {
						fmt.Printf("   %s: %s\n", h, val)
					}
				}

				fmt.Println()
				fmt.Println("Data:")
			}

			fmt.Printf("%s\n", string(m.Data))
			if !strings.HasSuffix(string(m.Data), "\n") {
				fmt.Println()
			}
		} else {
			fmt.Println(string(m.Data))
		}

		if c.ack {
			err = m.Respond(nil)
			if err != nil {
				fmt.Printf("Acknowledging message via subject %s failed: %s\n", m.Reply, err)
			}
		}
	}

	if consumer.DeliverGroup() == "" {
		_, err = c.nc.Subscribe(consumer.DeliverySubject(), handler)
	} else {
		_, err = c.nc.QueueSubscribe(consumer.DeliverySubject(), consumer.DeliverGroup(), handler)
	}

	fisk.FatalIfError(err, "could not subscribe")

	<-ctx.Done()

	return nil
}

func (c *consumerCmd) subAction(_ *fisk.ParseContext) error {
	c.connectAndSetup(true, true, nats.UseOldRequestStyle())

	consumer, err := c.mgr.LoadConsumer(c.stream, c.consumer)
	fisk.FatalIfError(err, "could not load Consumer")

	if consumer.AckPolicy() == api.AckNone {
		c.ack = false
	}

	switch {
	case consumer.IsPullMode():
		return c.getNextMsgDirect(consumer.StreamName(), consumer.Name())
	case consumer.IsPushMode():
		return c.subscribeConsumer(consumer)
	default:
		return fmt.Errorf("consumer %s > %s is in an unknown state", c.stream, c.consumer)
	}
}

func (c *consumerCmd) nextAction(_ *fisk.ParseContext) error {
	c.connectAndSetup(false, false, nats.UseOldRequestStyle())

	var err error

	for i := 0; i < c.pullCount; i++ {
		err = c.getNextMsgDirect(c.stream, c.consumer)
		if err != nil {
			break
		}
	}
	return err
}

func (c *consumerCmd) connectAndSetup(askStream bool, askConsumer bool, opts ...nats.Option) {
	var err error

	c.nc, c.mgr, err = prepareHelper("", append(natsOpts(), opts...)...)
	fisk.FatalIfError(err, "setup failed")

	if c.stream != "" && c.consumer != "" {
		c.selectedConsumer, err = c.mgr.LoadConsumer(c.stream, c.consumer)
		if err == nil {
			return
		}
	}

	if askStream {
		c.stream, _, err = selectStream(c.mgr, c.stream, c.force, c.showAll)
		fisk.FatalIfError(err, "could not select Stream")

		if askConsumer {
			c.consumer, c.selectedConsumer, err = selectConsumer(c.mgr, c.stream, c.consumer, c.force)
			fisk.FatalIfError(err, "could not select Consumer")
		}
	}
}

func (c *consumerCmd) reportAction(_ *fisk.ParseContext) error {
	c.connectAndSetup(true, false)

	s, err := c.mgr.LoadStream(c.stream)
	if err != nil {
		return err
	}

	ss, err := s.LatestState()
	if err != nil {
		return err
	}

	leaders := make(map[string]*raftLeader)

	table := newTableWriter(fmt.Sprintf("Consumer report for %s with %s consumers", c.stream, humanize.Comma(int64(ss.Consumers))))
	table.AddHeaders("Consumer", "Mode", "Ack Policy", "Ack Wait", "Ack Pending", "Redelivered", "Unprocessed", "Ack Floor", "Cluster")
	missing, err := s.EachConsumer(func(cons *jsm.Consumer) {
		cs, err := cons.LatestState()
		if err != nil {
			log.Printf("Could not obtain consumer state for %s: %s", cons.Name(), err)
			return
		}

		mode := "Push"
		if cons.IsPullMode() {
			mode = "Pull"
		}

		if cs.Cluster != nil {
			if cs.Cluster.Leader != "" {
				_, ok := leaders[cs.Cluster.Leader]
				if !ok {
					leaders[cs.Cluster.Leader] = &raftLeader{name: cs.Cluster.Leader, cluster: cs.Cluster.Name}
				}
				leaders[cs.Cluster.Leader].groups++
			}
		}

		if c.raw {
			table.AddRow(cons.Name(), mode, cons.AckPolicy().String(), cons.AckWait(), cs.NumAckPending, cs.NumRedelivered, cs.NumPending, cs.AckFloor.Stream, renderCluster(cs.Cluster))
		} else {
			unprocessed := "0"
			if cs.NumPending > 0 {
				upct := math.Floor(float64(cs.NumPending) / float64(ss.Msgs) * 100)
				if upct > 100 {
					upct = 100
				}
				unprocessed = fmt.Sprintf("%s / %0.0f%%", humanize.Comma(int64(cs.NumPending)), upct)
			}

			table.AddRow(cons.Name(), mode, cons.AckPolicy().String(), humanizeDuration(cons.AckWait()), humanize.Comma(int64(cs.NumAckPending)), humanize.Comma(int64(cs.NumRedelivered)), unprocessed, humanize.Comma(int64(cs.AckFloor.Stream)), renderCluster(cs.Cluster))
		}
	})
	if err != nil {
		return err
	}

	fmt.Println(table.Render())

	if c.reportLeaderDistrib && len(leaders) > 0 {
		renderRaftLeaders(leaders, "Consumers")
	}

	if len(missing) > 0 {
		c.renderMissing(os.Stdout, missing)
	}

	return nil
}

func (c *consumerCmd) renderMissing(out io.Writer, missing []string) {
	toany := func(items []string) (res []any) {
		for _, i := range items {
			res = append(res, any(i))
		}
		return res
	}

	if len(missing) > 0 {
		fmt.Fprintln(out)
		sort.Strings(missing)
		table := newTableWriter("Inaccessible Consumers")
		sliceGroups(missing, 4, func(names []string) {
			table.AddRow(toany(names)...)
		})
		fmt.Fprint(out, table.Render())
	}
}
