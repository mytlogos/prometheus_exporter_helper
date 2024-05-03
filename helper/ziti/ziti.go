package ziti

import (
	"net"
	"os"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/go-kit/log/level"
	"github.com/mytlogos/prometheus_exporter_helper/helper"
	"github.com/openziti/sdk-golang/ziti"
)

type zitiFlagConfig struct {
	IdentityFile *string
	ServiceName  *string
	ZitiOnly     *bool
}

type ZitiServerHelper struct {
	zitiConfig     *zitiFlagConfig
	ExporterHelper *helper.ExporterHelper
}

// InitFlags implements ServerHelper.
func (z *ZitiServerHelper) InitFlags() {
	z.zitiConfig = &zitiFlagConfig{}
	z.zitiConfig.IdentityFile = kingpin.Flag(
		"web.ziti.identity", "Path of the ziti identity json file. Ignored if path does not exist",
	).Default("./identity.json").String()
	z.zitiConfig.ServiceName = kingpin.Flag(
		"web.ziti.service-name", "Name of the service to bind to. Stops if it wants to bind but does not exist",
	).Default(z.ExporterHelper.ExporterName).String()
	z.zitiConfig.ZitiOnly = kingpin.Flag(
		"web.ziti.only", "If it listens on the ziti network only. Requires a valid ziti config.",
	).Default("false").Bool()
}

// CreateListener implements ServerHelper.
func (z *ZitiServerHelper) CreateListener() net.Listener {
	options := ziti.ListenOptions{
		ConnectTimeout: 5 * time.Minute,
		MaxConnections: 3,
	}

	if stat, err := os.Stat(*z.zitiConfig.IdentityFile); err != nil || stat.IsDir() {
		if err != nil {
			level.Warn(z.ExporterHelper.Logger()).Log("err", err)
		}
		level.Warn(z.ExporterHelper.Logger()).Log("msg", "identity file likely not accessible - ignoring")
		return nil
	}

	// Get identity config
	cfg, err := ziti.NewConfigFromFile(*z.zitiConfig.IdentityFile)

	if err != nil {
		panic(err)
	}

	ctx, err := ziti.NewContext(cfg)

	if err != nil {
		panic(err)
	}

	listener, err := ctx.ListenWithOptions(*z.zitiConfig.ServiceName, &options)

	if err != nil {
		level.Error(z.ExporterHelper.Logger()).Log("msg", "error binding service", "err", err)
		os.Exit(1)
	}

	level.Info(z.ExporterHelper.Logger()).Log("msg", "listening for requests", "service", z.zitiConfig.ServiceName)
	return listener
}

// IsOnlyListener implements ServerHelper.
func (z *ZitiServerHelper) IsOnlyListener() bool {
	return *z.zitiConfig.ZitiOnly
}

var _ helper.ServerHelper = (*ZitiServerHelper)(nil)
