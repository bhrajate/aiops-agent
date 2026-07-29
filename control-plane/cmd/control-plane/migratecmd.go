package main

// migrate 子命令:业务库 schema 版本管理。
//
// 与常驻进程共用同一个二进制/镜像,保证 SQL 与代码同版本(见 internal/migrate)。
// 生产由 Helm pre-install/pre-upgrade Job 调用 `control-plane migrate up`。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/aiops/control-plane/internal/config"
	"github.com/aiops/control-plane/internal/migrate"
)

// runMigrateCmd 处理 `control-plane migrate <up|down|version|force N>`。
// 它自行决定进程退出码,不返回。
func runMigrateCmd(log *slog.Logger, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, migrateUsage)
		os.Exit(2)
	}
	cfg := config.Load()
	mg, err := migrate.New(cfg.DBDSN)
	if err != nil {
		log.Error("migrator init failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = mg.Close() }()

	switch args[0] {
	case "up":
		before, _, _ := mg.Version()
		if err := mg.Up(); err != nil {
			log.Error("migrate up failed", "err", err)
			os.Exit(1)
		}
		after, dirty, _ := mg.Version()
		if before == after {
			log.Info("schema already up to date", "version", after)
		} else {
			log.Info("schema migrated", "from", before, "to", after, "dirty", dirty)
		}

	case "down":
		// 单步回滚。一次性删库不应该只差一条命令,故不提供 down-all。
		before, _, _ := mg.Version()
		if err := mg.Down(); err != nil {
			log.Error("migrate down failed", "err", err)
			os.Exit(1)
		}
		after, _, _ := mg.Version()
		log.Warn("schema rolled back one step", "from", before, "to", after)

	case "version":
		v, dirty, err := mg.Version()
		if err != nil {
			log.Error("read schema version failed", "err", err)
			os.Exit(1)
		}
		fmt.Printf("current=%d expected=%d dirty=%v\n", v, migrate.Expected, dirty)
		// 版本不匹配用非零退出码表达,便于在 CI / 探针脚本里直接判断。
		if v != migrate.Expected || dirty {
			os.Exit(3)
		}

	case "force":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "force 需要版本号参数,例如:migrate force 3")
			os.Exit(2)
		}
		v, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "非法版本号 %q\n", args[1])
			os.Exit(2)
		}
		if err := mg.Force(v); err != nil {
			log.Error("migrate force failed", "err", err)
			os.Exit(1)
		}
		log.Warn("schema version forced (未执行任何 SQL,仅改版本表)", "version", v)

	default:
		fmt.Fprintln(os.Stderr, migrateUsage)
		os.Exit(2)
	}
}

// ensureSchema 校验业务库 schema 版本是否与本二进制匹配。
//
// 默认只校验。AIOPS_AUTO_MIGRATE=true 时先自动迁移再校验——仅建议用于开发与
// 单副本部署:生产多副本滚动更新期间自动迁移会让旧副本面对新 schema。
func ensureSchema(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	mg, err := migrate.New(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("初始化迁移器: %w", err)
	}
	defer func() { _ = mg.Close() }()

	if cfg.AutoMigrate {
		if cfg.IsProduction() {
			log.Warn("AIOPS_AUTO_MIGRATE=true 且为生产模式:" +
				"多副本滚动更新下自动迁移可能让旧副本面对新 schema," +
				"建议改用 Helm pre-upgrade Job 执行 `control-plane migrate up`")
		}
		before, _, _ := mg.Version()
		if err := mg.Up(); err != nil {
			return fmt.Errorf("自动迁移: %w", err)
		}
		if after, _, _ := mg.Version(); after != before {
			log.Info("schema auto-migrated", "from", before, "to", after)
		}
	}

	v, dirty, err := mg.Version()
	if err != nil {
		return fmt.Errorf("读取 schema 版本: %w", err)
	}
	if dirty || v != migrate.Expected {
		return &migrate.ErrVersionMismatch{Current: v, Expected: migrate.Expected, Dirty: dirty}
	}
	log.Info("schema version verified", "version", v)
	return nil
}

const migrateUsage = `用法: control-plane migrate <command>

  up            应用所有待执行迁移(幂等;已最新则无操作)
  down          回滚一个版本
  version       打印当前/期望版本;不匹配或 dirty 时退出码 3
  force <N>     强制标记版本为 N 并清除 dirty(不执行 SQL,仅用于人工修复后对齐)

数据库地址取自 AIOPS_DB_DSN。`
