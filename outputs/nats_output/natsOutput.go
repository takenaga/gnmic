package nats_output

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/karimra/gnmic/formatters"
	"github.com/karimra/gnmic/outputs"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
)

const (
	natsConnectWait         = 2 * time.Second
	natsReconnectBufferSize = 100 * 1024 * 1024
	defaultSubjectName      = "telemetry"
	defaultFormat           = "json"
	defaultNumWorkers       = 1
	defaultWriteTimeout     = 10 * time.Second
	loggingPrefix           = "nats_output "
)

func init() {
	outputs.Register("nats", func() outputs.Output {
		return &NatsOutput{
			Cfg:    &Config{},
			wg:     new(sync.WaitGroup),
			logger: log.New(ioutil.Discard, loggingPrefix, log.LstdFlags|log.Lmicroseconds),
		}
	})
}

type protoMsg struct {
	m    proto.Message
	meta outputs.Meta
}

// NatsOutput //
type NatsOutput struct {
	Cfg      *Config
	ctx      context.Context
	cancelFn context.CancelFunc
	msgChan  chan *protoMsg
	wg       *sync.WaitGroup
	logger   *log.Logger
	mo       *formatters.MarshalOptions
	evps     []formatters.EventProcessor
}

// Config //
type Config struct {
	Name            string        `mapstructure:"name,omitempty"`
	Address         string        `mapstructure:"address,omitempty"`
	SubjectPrefix   string        `mapstructure:"subject-prefix,omitempty"`
	Subject         string        `mapstructure:"subject,omitempty"`
	Username        string        `mapstructure:"username,omitempty"`
	Password        string        `mapstructure:"password,omitempty"`
	ConnectTimeWait time.Duration `mapstructure:"connect-time-wait,omitempty"`
	Format          string        `mapstructure:"format,omitempty"`
	NumWorkers      int           `mapstructure:"num-workers,omitempty"`
	WriteTimeout    time.Duration `mapstructure:"write-timeout,omitempty"`
	Debug           bool          `mapstructure:"debug,omitempty"`
	EnableMetrics   bool          `mapstructure:"enable-metrics,omitempty"`
	EventProcessors []string      `mapstructure:"event-processors,omitempty"`
}

func (n *NatsOutput) String() string {
	b, err := json.Marshal(n)
	if err != nil {
		return ""
	}
	return string(b)
}
func (n *NatsOutput) SetLogger(logger *log.Logger) {
	if logger != nil && n.logger != nil {
		n.logger.SetOutput(logger.Writer())
		n.logger.SetFlags(logger.Flags())
	}
}

func (n *NatsOutput) SetEventProcessors(ps map[string]map[string]interface{}, log *log.Logger) {
	for _, epName := range n.Cfg.EventProcessors {
		if epCfg, ok := ps[epName]; ok {
			epType := ""
			for k := range epCfg {
				epType = k
				break
			}
			if in, ok := formatters.EventProcessors[epType]; ok {
				ep := in()
				err := ep.Init(epCfg[epType], log)
				if err != nil {
					n.logger.Printf("failed initializing event processor '%s' of type='%s': %v", epName, epType, err)
					continue
				}
				n.evps = append(n.evps, ep)
				n.logger.Printf("added event processor '%s' of type=%s to nats output", epName, epType)
			}
		}
	}
}

// Init //
func (n *NatsOutput) Init(ctx context.Context, cfg map[string]interface{}, opts ...outputs.Option) error {
	err := outputs.DecodeConfig(cfg, n.Cfg)
	if err != nil {
		return err
	}
	err = n.setDefaults()
	if err != nil {
		return err
	}
	for _, opt := range opts {
		opt(n)
	}
	n.msgChan = make(chan *protoMsg)
	initMetrics()
	n.mo = &formatters.MarshalOptions{Format: n.Cfg.Format}
	n.ctx, n.cancelFn = context.WithCancel(ctx)
	n.wg.Add(n.Cfg.NumWorkers)
	for i := 0; i < n.Cfg.NumWorkers; i++ {
		cfg := *n.Cfg
		cfg.Name = fmt.Sprintf("%s-%d", cfg.Name, i)
		go n.worker(ctx, i, &cfg)
	}

	go func() {
		<-ctx.Done()
		n.Close()
	}()
	return nil
}

func (n *NatsOutput) setDefaults() error {
	if n.Cfg.ConnectTimeWait <= 0 {
		n.Cfg.ConnectTimeWait = natsConnectWait
	}
	if n.Cfg.Subject == "" && n.Cfg.SubjectPrefix == "" {
		n.Cfg.Subject = defaultSubjectName
	}
	if n.Cfg.Format == "" {
		n.Cfg.Format = defaultFormat
	}
	if !(n.Cfg.Format == "event" || n.Cfg.Format == "protojson" || n.Cfg.Format == "proto" || n.Cfg.Format == "json") {
		return fmt.Errorf("unsupported output format '%s' for output type NATS", n.Cfg.Format)
	}
	if n.Cfg.Name == "" {
		n.Cfg.Name = "gnmic-" + uuid.New().String()
	}
	if n.Cfg.NumWorkers <= 0 {
		n.Cfg.NumWorkers = defaultNumWorkers
	}
	if n.Cfg.WriteTimeout <= 0 {
		n.Cfg.WriteTimeout = defaultWriteTimeout
	}
	return nil
}

// Write //
func (n *NatsOutput) Write(ctx context.Context, rsp proto.Message, meta outputs.Meta) {
	if rsp == nil || n.mo == nil {
		return
	}

	wctx, cancel := context.WithTimeout(ctx, n.Cfg.WriteTimeout)
	defer cancel()

	select {
	case <-ctx.Done():
		return
	case n.msgChan <- &protoMsg{m: rsp, meta: meta}:
	case <-wctx.Done():
		if n.Cfg.Debug {
			n.logger.Printf("writing expired after %s, NATS output might not be initialized", n.Cfg.WriteTimeout)
		}
		if n.Cfg.EnableMetrics {
			NatsNumberOfFailSendMsgs.WithLabelValues(n.Cfg.Name, "timeout").Inc()
		}
		return
	}
}

// Close //
func (n *NatsOutput) Close() error {
	//	n.conn.Close()
	n.cancelFn()
	n.wg.Wait()
	return nil
}

// Metrics //
func (n *NatsOutput) RegisterMetrics(reg *prometheus.Registry) {
	if !n.Cfg.EnableMetrics {
		return
	}
	if err := registerMetrics(reg); err != nil {
		n.logger.Printf("failed to register metric: %+v", err)
	}
}

func (n *NatsOutput) createNATSConn(c *Config) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name(c.Name),
		nats.SetCustomDialer(n),
		nats.ReconnectWait(n.Cfg.ConnectTimeWait),
		nats.ReconnectBufSize(natsReconnectBufferSize),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			n.logger.Printf("NATS error: %v", err)
		}),
		nats.DisconnectHandler(func(c *nats.Conn) {
			n.logger.Println("Disconnected from NATS")
		}),
		nats.ClosedHandler(func(c *nats.Conn) {
			n.logger.Println("NATS connection is closed")
		}),
	}
	if c.Username != "" && c.Password != "" {
		opts = append(opts, nats.UserInfo(c.Username, c.Password))
	}
	nc, err := nats.Connect(c.Address, opts...)
	if err != nil {
		return nil, err
	}
	return nc, nil
}

// Dial //
func (n *NatsOutput) Dial(network, address string) (net.Conn, error) {
	ctx, cancel := context.WithCancel(n.ctx)
	defer cancel()

	for {
		n.logger.Printf("attempting to connect to %s", address)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		select {
		case <-n.ctx.Done():
			return nil, n.ctx.Err()
		default:
			d := &net.Dialer{}
			if conn, err := d.DialContext(ctx, network, address); err == nil {
				n.logger.Printf("successfully connected to NATS server %s", address)
				return conn, nil
			}
			time.Sleep(n.Cfg.ConnectTimeWait)
		}
	}
}

func (n *NatsOutput) worker(ctx context.Context, i int, cfg *Config) {
	defer n.wg.Done()
	var natsConn *nats.Conn
	var err error
	workerLogPrefix := fmt.Sprintf("worker-%d", i)
	n.logger.Printf("%s starting", workerLogPrefix)
CRCONN:
	natsConn, err = n.createNATSConn(cfg)
	if err != nil {
		n.logger.Printf("%s failed to create connection: %v", workerLogPrefix, err)
		time.Sleep(n.Cfg.ConnectTimeWait)
		goto CRCONN
	}
	defer natsConn.Close()
	n.logger.Printf("%s initialized nats producer: %+v", workerLogPrefix, cfg)
	for {
		select {
		case <-ctx.Done():
			n.logger.Printf("%s flushing", workerLogPrefix)
			natsConn.FlushTimeout(time.Second)
			n.logger.Printf("%s shutting down", workerLogPrefix)
			return
		case m := <-n.msgChan:
			b, err := n.mo.Marshal(m.m, m.meta, n.evps...)
			if err != nil {
				if n.Cfg.Debug {
					n.logger.Printf("%s failed marshaling proto msg: %v", workerLogPrefix, err)
				}
				if n.Cfg.EnableMetrics {
					NatsNumberOfFailSendMsgs.WithLabelValues(cfg.Name, "marshal_error").Inc()
				}
				continue
			}
			subject := n.subjectName(cfg, m.meta)
			var start time.Time
			if n.Cfg.EnableMetrics {
				start = time.Now()
			}
			err = natsConn.Publish(subject, b)
			if err != nil {
				if n.Cfg.Debug {
					n.logger.Printf("%s failed to write to nats subject '%s': %v", workerLogPrefix, subject, err)
				}
				if n.Cfg.EnableMetrics {
					NatsNumberOfFailSendMsgs.WithLabelValues(cfg.Name, "publish_error").Inc()
				}
				natsConn.Close()
				time.Sleep(cfg.ConnectTimeWait)
				goto CRCONN
			}
			if n.Cfg.EnableMetrics {
				NatsSendDuration.WithLabelValues(cfg.Name).Set(float64(time.Since(start).Nanoseconds()))
				NatsNumberOfSentMsgs.WithLabelValues(cfg.Name, subject).Inc()
				NatsNumberOfSentBytes.WithLabelValues(cfg.Name, subject).Add(float64(len(b)))
			}
		}
	}
}

func (n *NatsOutput) subjectName(c *Config, meta outputs.Meta) string {
	if c.SubjectPrefix != "" {
		ssb := strings.Builder{}
		ssb.WriteString(n.Cfg.SubjectPrefix)
		if s, ok := meta["source"]; ok {
			source := strings.ReplaceAll(s, ".", "-")
			source = strings.ReplaceAll(source, " ", "_")
			ssb.WriteString(".")
			ssb.WriteString(source)
		}
		if subname, ok := meta["subscription-name"]; ok {
			ssb.WriteString(".")
			ssb.WriteString(subname)
		}
		return strings.ReplaceAll(ssb.String(), " ", "_")
	}
	return strings.ReplaceAll(n.Cfg.Subject, " ", "_")
}
