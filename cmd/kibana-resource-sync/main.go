package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"kibana-resource-sync/internal/config"
	"kibana-resource-sync/internal/logging"
	"kibana-resource-sync/internal/metrics"
	syncer "kibana-resource-sync/internal/sync"
)

func main() {
	var (
		configPath    string
		dryRun        bool
		logLevel      string
		reconcileMode string
		driftMode     string
	)

	flag.StringVar(&configPath, "config", "", "Path to YAML config file")
	flag.BoolVar(&dryRun, "dry-run", false, "Plan changes but do not modify any target instance")
	flag.StringVar(&logLevel, "log-level", "info", "Log level: debug|info|warn|error")
	flag.StringVar(&reconcileMode, "reconcile-mode", "", "Override reconcile mode from config: delete|disable")
	flag.StringVar(&driftMode, "drift-mode", "", "Override drift mode from config: overwrite|flag")
	flag.Parse()

	if strings.TrimSpace(configPath) == "" {
		fmt.Fprintln(os.Stderr, "-config is required")
		os.Exit(2)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	if reconcileMode != "" {
		mode := strings.ToLower(strings.TrimSpace(reconcileMode))
		if mode != "delete" && mode != "disable" {
			fmt.Fprintf(os.Stderr, "invalid -reconcile-mode %q; expected delete or disable\n", reconcileMode)
			os.Exit(2)
		}
		cfg.ReconcileMode = mode
	}
	if driftMode != "" {
		mode := strings.ToLower(strings.TrimSpace(driftMode))
		if mode != "overwrite" && mode != "flag" {
			fmt.Fprintf(os.Stderr, "invalid -drift-mode %q; expected overwrite or flag\n", driftMode)
			os.Exit(2)
		}
		cfg.DriftMode = mode
	}

	logger, err := logging.New(logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure logger: %v\n", err)
		os.Exit(1)
	}
	logger.Info("loaded configuration",
		"config_path", config.AbsPath(configPath),
		"dry_run", dryRun,
		"environment_routing", cfg.UsesEnvironmentTargets(),
		"reconcile_mode", cfg.ReconcileMode,
		"drift_mode", cfg.DriftMode,
	)
	logger.Debug("runtime overrides applied",
		"reconcile_mode_override", strings.TrimSpace(reconcileMode) != "",
		"drift_mode_override", strings.TrimSpace(driftMode) != "",
	)

	pushGatewayCfg, pushGatewayEnabled, err := metrics.LoadPushGatewayConfigFromEnv()
	if err != nil {
		logger.Error("configure pushgateway metrics", "error", err.Error())
		os.Exit(1)
	}
	if pushGatewayEnabled {
		logger.Info("pushgateway metrics enabled",
			"pushgateway_url", pushGatewayCfg.URL,
			"pushgateway_job", pushGatewayCfg.Job,
			"pushgateway_instance", pushGatewayCfg.Instance,
			"pushgateway_timeout", pushGatewayCfg.Timeout.String(),
		)
	}

	job, err := syncer.New(cfg, logger, syncer.Options{DryRun: dryRun})
	if err != nil {
		logger.Error("create sync runner", "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pushRunMetrics := func(runMetrics metrics.RunMetrics, success bool) {
		if !pushGatewayEnabled {
			return
		}
		if err := metrics.PushRunMetrics(context.Background(), pushGatewayCfg, runMetrics, success, time.Now().UTC()); err != nil {
			logger.Error("pushgateway metrics push failed", "error", err.Error())
			return
		}
		logger.Debug("pushgateway metrics pushed", "success", success)
	}

	if err := job.Run(ctx); err != nil {
		logger.Error("sync run failed", "error", err.Error())
		runMetrics := job.Metrics()
		logger.Info("run summary", "metrics", runMetrics)
		pushRunMetrics(runMetrics, false)
		os.Exit(1)
	}

	runMetrics := job.Metrics()
	logger.Info("run summary", "metrics", runMetrics)
	pushRunMetrics(runMetrics, true)
}
