package helper

import (
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/coreos/go-systemd/activation"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/openziti/sdk-golang/ziti"
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
	ExporterName   string
	Description    string
	DefaultAddress string
	metricsPath    *string
	toolkitFlags   *web.FlagConfig
	promlogConfig  *promlog.Config
	zitiConfig     *zitiFlagConfig
	logger         log.Logger
}

func NewHelper(name, description, address string) ExporterHelper {
	return ExporterHelper{
		ExporterName:   name,
		Description:    description,
		DefaultAddress: address,
	}
}

func (e *ExporterHelper) InitFlags() {
	e.metricsPath = kingpin.Flag(
		"web.telemetry-path", "Path under which to expose metrics",
	).Default("/metrics").String()

	e.toolkitFlags = webflag.AddFlags(kingpin.CommandLine, ":9633")

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
	).Bool()
}

func (e *ExporterHelper) Logger() log.Logger {
	if e.logger == nil {
		e.logger = promlog.New(e.promlogConfig)
	}
	return e.logger
}

func (e *ExporterHelper) ListenAndServe() {
	e.ListenAndServeHandler(promhttp.Handler())
}

func (e *ExporterHelper) ListenAndServeHandler(promHandler http.Handler) {
	logger := e.Logger()

	level.Info(logger).Log("msg", "Starting "+e.ExporterName, "version", version.Info())
	level.Info(logger).Log("msg", "Build context", "build_context", version.BuildContext())
	http.Handle(*e.metricsPath, promHandler)

	if *e.metricsPath != "/" && *e.metricsPath != "" {
		landingConfig := web.LandingConfig{
			Name:        e.ExporterName,
			Description: e.Description,
			Version:     version.Info(),
			Links: []web.LandingLinks{
				{
					Address: *e.metricsPath,
					Text:    "Metrics",
				},
			},
		}
		landingPage, err := web.NewLandingPage(landingConfig)
		if err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
		http.Handle("/", landingPage)
	}

	srv := &http.Server{}

	if err := e.listenAndServe(srv); err != nil {
		level.Error(logger).Log("err", err)
		os.Exit(1)
	}
}

func (e *ExporterHelper) zitiListener() net.Listener {
	options := ziti.ListenOptions{
		ConnectTimeout: 5 * time.Minute,
		MaxConnections: 3,
	}

	if stat, err := os.Stat(*e.zitiConfig.IdentityFile); err != nil || stat.IsDir() {
		if err != nil {
			level.Error(e.logger).Log("err", err)
		}
		level.Error(e.logger).Log("msg", "identity file likely not accessible - ignoring")
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

func (e *ExporterHelper) listenAndServe(server *http.Server) error {
	logger := e.Logger()

	if *e.zitiConfig.ZitiOnly {
		listener := e.zitiListener()

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
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return err
		}
		defer listener.Close()
		listeners = append(listeners, listener)
	}

	listener := e.zitiListener()

	if listener != nil {
		listeners = append(listeners, listener)
	}
	return web.ServeMultiple(listeners, server, e.toolkitFlags, logger)
}
