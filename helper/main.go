package helper

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/coreos/go-systemd/activation"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/openziti/sdk-golang/ziti"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versionCollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	webflag "github.com/prometheus/exporter-toolkit/web/kingpinflag"
)

type zitiFlagConfig struct {
	IdentityFile *string
	ServiceName  *string
	ZitiOnly     *bool
}

type ExporterHelper struct {
	// what name to use for the exporter
	// must be of format [a-ZA-Z0-9_], hyphens "-" are not allowed
	ExporterName string
	// help description of the program
	Description string
	// the address it will listen on by default if not otherwise specified
	// should have the form of "<interface-address>:<port>"
	// the form ":<port>" will use all available interfaces
	DefaultAddress string
	// for interopability with other server like things like gonic
	// by default it uses http.Handle and thus the default mux
	HandlerSetter func(string, http.Handler)
	// path to the metrics, defaults to /metrics
	MetricsPath *string
	// disable metrics about the process itself, by default false
	DisableExporterMetrics *bool
	// enable the landing page for path '/', by default true
	LandingPage *bool
	// the maximum number of concurrent scrape requests
	MaxRequests *int

	toolkitFlags  *web.FlagConfig
	promlogConfig *promlog.Config
	zitiConfig    *zitiFlagConfig
	logger        log.Logger
}

func NewHelper(name, description, address string) ExporterHelper {
	return ExporterHelper{
		ExporterName:   name,
		Description:    description,
		DefaultAddress: address,
		HandlerSetter:  http.Handle,
	}
}

func (e *ExporterHelper) InitFlags() {
	e.MetricsPath = kingpin.Flag(
		"web.telemetry-path", "Path under which to expose metrics",
	).Default("/metrics").String()

	e.toolkitFlags = webflag.AddFlags(kingpin.CommandLine, e.DefaultAddress)

	e.promlogConfig = &promlog.Config{}
	flag.AddFlags(kingpin.CommandLine, e.promlogConfig)
	kingpin.Version(version.Print(e.ExporterName))
	kingpin.HelpFlag.Short('h')

	e.zitiConfig = &zitiFlagConfig{}
	e.zitiConfig.IdentityFile = kingpin.Flag(
		"web.ziti.identity", "Path of the ziti identity json file. Ignored if path does not exist",
	).Default("./identity.json").String()
	e.zitiConfig.ServiceName = kingpin.Flag(
		"web.ziti.service-name", "Name of the service to bind to. Stops if it wants to bind but does not exist",
	).Default(e.ExporterName).String()
	e.zitiConfig.ZitiOnly = kingpin.Flag(
		"web.ziti.only", "If it listens on the ziti network only. Requires a valid ziti config.",
	).Default("false").Bool()

	e.DisableExporterMetrics = kingpin.Flag(
		"web.disable-exporter-metrics",
		"Exclude metrics about the exporter itself (promhttp_*, process_*, go_*).",
	).Bool()
	e.LandingPage = kingpin.Flag(
		"web.landing-page",
		"Enable or disable the landing page on root path '/'",
	).Default("true").Bool()
	e.MaxRequests = kingpin.Flag(
		"web.max-requests",
		"Maximum number of parallel scrape requests. Use 0 to disable.",
	).Default("2").Int()
}

func (e *ExporterHelper) Logger() log.Logger {
	if e.logger == nil {
		e.logger = promlog.New(e.promlogConfig)
	}
	return e.logger
}

// create the prometheus handler and register the collector if not nil
func (e *ExporterHelper) CreatePromHandler(collector prometheus.Collector) http.Handler {
	r := prometheus.NewRegistry()
	r.MustRegister(versionCollector.NewCollector(e.ExporterName))

	if collector != nil {
		if err := r.Register(collector); err != nil {
			level.Error(e.logger).Log("msg", "Couldn't register exporter collector", "err", err)
			os.Exit(1)
		}
	}

	handler := promhttp.HandlerFor(
		prometheus.Gatherers{r},
		promhttp.HandlerOpts{
			ErrorHandling:       promhttp.ContinueOnError,
			MaxRequestsInFlight: *e.MaxRequests,
			EnableOpenMetrics:   true,
		},
	)

	if !*e.DisableExporterMetrics {
		handler = promhttp.InstrumentMetricHandler(
			r, handler,
		)
		r.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		)
	}
	return handler
}

// use the prometheus handler configured via flags
// a nil collector will be ignored
func (e *ExporterHelper) ListenAndServeCollector(collector prometheus.Collector) {
	handler := e.CreatePromHandler(collector)
	e.ListenAndServeHandler(handler)
}

func (e *ExporterHelper) ListenAndServeHandler(promHandler http.Handler) {
	srv := &http.Server{}

	if err := e.ListenAndServe(srv, promHandler); err != nil {
		level.Error(e.logger).Log("err", err)
		os.Exit(1)
	}
}

func (e *ExporterHelper) ListenAndServe(server *http.Server, promHandler http.Handler) error {
	logger := e.Logger()

	level.Info(logger).Log("msg", "Starting "+e.ExporterName, "version", version.Info())
	level.Info(logger).Log("msg", "Build context", "build_context", version.BuildContext())

	e.HandlerSetter(*e.MetricsPath, promHandler)

	if *e.MetricsPath != "/" && *e.MetricsPath != "" && *e.LandingPage {
		landingConfig := web.LandingConfig{
			Name:        e.ExporterName,
			Description: e.Description,
			Version:     version.Info(),
			Links: []web.LandingLinks{
				{
					Address: *e.MetricsPath,
					Text:    "Metrics",
				},
			},
		}
		landingPage, err := web.NewLandingPage(landingConfig)
		if err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
		e.HandlerSetter("/", landingPage)
	}

	if err := e.listenAndServe(server); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (e *ExporterHelper) CreateZitiListener() net.Listener {
	options := ziti.ListenOptions{
		ConnectTimeout: 5 * time.Minute,
		MaxConnections: 3,
	}

	if stat, err := os.Stat(*e.zitiConfig.IdentityFile); err != nil || stat.IsDir() {
		if err != nil {
			level.Warn(e.logger).Log("err", err)
		}
		level.Warn(e.logger).Log("msg", "identity file likely not accessible - ignoring")
		return nil
	}

	// Get identity config
	cfg, err := ziti.NewConfigFromFile(*e.zitiConfig.IdentityFile)

	if err != nil {
		panic(err)
	}

	ctx, err := ziti.NewContext(cfg)

	if err != nil {
		panic(err)
	}

	listener, err := ctx.ListenWithOptions(*e.zitiConfig.ServiceName, &options)

	if err != nil {
		level.Error(e.logger).Log("msg", "error binding service", "err", err)
		os.Exit(1)
	}

	level.Info(e.logger).Log("msg", "listening for requests", "service", e.zitiConfig.ServiceName)
	return listener
}

func (e *ExporterHelper) createListener(address string) (net.Listener, error) {
	listenType := "tcp"

	// check if unix socket
	if strings.HasPrefix(address, "/") && (strings.HasSuffix(address, ".socket") || strings.HasSuffix(address, ".sock")) {
		listenType = "unix"

		// Cleanup the sockfile.
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-c
			os.Remove(address)
			os.Exit(1)
		}()

		// if we exit "normally" without SIGTERM signal, cleanup too
		defer func() {
			removeErr := os.Remove(address)

			if removeErr != nil {
				level.Warn(e.logger).Log("msg", fmt.Sprintf("Could not remove unix socket: %s", address))
			}
		}()
	}
	return net.Listen(listenType, address)
}

func (e *ExporterHelper) listenAndServe(server *http.Server) error {
	logger := e.Logger()

	if *e.zitiConfig.ZitiOnly {
		listener := e.CreateZitiListener()

		if listener == nil {
			level.Error(logger).Log("msg", "could not create ziti listener in ziti only mode")
			os.Exit(1)
		}
		return web.ServeMultiple([]net.Listener{listener}, server, e.toolkitFlags, logger)
	}

	if e.toolkitFlags.WebSystemdSocket == nil && (e.toolkitFlags.WebListenAddresses == nil || len(*e.toolkitFlags.WebListenAddresses) == 0) {
		return web.ErrNoListeners
	}

	if e.toolkitFlags.WebSystemdSocket != nil && *e.toolkitFlags.WebSystemdSocket {
		level.Info(logger).Log("msg", "Listening on systemd activated listeners instead of port listeners.")
		listeners, err := activation.Listeners()
		if err != nil {
			return err
		}
		if len(listeners) < 1 {
			return errors.New("no socket activation file descriptors found")
		}
		return web.ServeMultiple(listeners, server, e.toolkitFlags, logger)
	}

	listeners := make([]net.Listener, 0, len(*e.toolkitFlags.WebListenAddresses))

	for _, address := range *e.toolkitFlags.WebListenAddresses {
		listener, err := e.createListener(address)
		if err != nil {
			return err
		}
		defer listener.Close()
		listeners = append(listeners, listener)
	}

	listener := e.CreateZitiListener()

	if listener != nil {
		listeners = append(listeners, listener)
	}
	return web.ServeMultiple(listeners, server, e.toolkitFlags, logger)
}
