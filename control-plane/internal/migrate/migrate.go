// Package migrate 管理业务库 schema 版本。
//
// 为什么需要它:此前 DDL 只通过 docker-compose 把 shared/sql 挂到 postgres 的
// /docker-entrypoint-initdb.d 执行,而该目录**只在数据卷首次创建时**生效。生产用
// 托管 PostgreSQL(见 deploy/DEPLOY.md)根本没有这个钩子,于是:
//   - 首次部署:表不存在,控制面起不来;
//   - 后续升级:没有任何地方记录"这个库跑到哪一版了",靠人记等于早晚漏跑或重跑。
//
// 设计取舍:
//
//  1. **迁移与启动分离。** 迁移是独立步骤(Helm pre-install/pre-upgrade Job 或 CI),
//     不在控制面启动时自动执行——多副本同时启动会争抢迁移,即使有锁也只是排队,
//     且滚动更新期间新旧版本共存,自动迁移会让旧副本面对新 schema。
//     控制面启动时只**校验**版本,落后就拒绝启动(fail-fast),而不是带着
//     不匹配的 schema 跑到第一次查询才炸。
//
//  2. **SQL 内嵌进二进制。** go:embed 保证迁移文件与代码同版本发布,镜像里不需要
//     额外挂载,Job 与控制面用同一个镜像同一份 SQL。
//
//  3. **迁移文件放在控制面模块内。** 它们必须在 go:embed 的模块边界内;而且控制面
//     是业务库的唯一写入方,schema 归它管是符合所有权的。开发种子数据留在
//     shared/seed(不属于 schema,生产不执行)。
package migrate

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// migrationsFS 内嵌全部迁移文件,保证与二进制同版本。
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Expected 是本二进制期望的 schema 版本,等于 migrations/ 下最大的迁移号。
//
// 新增迁移时**必须**同步 +1。TestExpectedMatchesFiles 会校验二者一致,
// 忘记改就会在 CI 失败,而不是在生产启动时才发现。
const Expected uint = 7

// ErrVersionMismatch 表示库的 schema 版本与本二进制期望的不一致。
type ErrVersionMismatch struct {
	Current  uint
	Expected uint
	Dirty    bool
}

func (e *ErrVersionMismatch) Error() string {
	if e.Dirty {
		return fmt.Sprintf(
			"数据库 schema 处于 dirty 状态(版本 %d):上一次迁移执行失败且未回滚。"+
				"需人工介入:检查该版本的 SQL 是否部分生效,修复后执行 "+
				"`control-plane migrate force <version>` 标记正确版本",
			e.Current)
	}
	if e.Current < e.Expected {
		return fmt.Sprintf(
			"数据库 schema 版本落后(当前 %d,需要 %d):请先执行迁移 "+
				"`control-plane migrate up`(生产由 Helm pre-upgrade Job 完成)",
			e.Current, e.Expected)
	}
	return fmt.Sprintf(
		"数据库 schema 版本超前(当前 %d,本二进制期望 %d):"+
			"通常意味着正在回滚到旧版本镜像,但 schema 未一起回滚。"+
			"确认新版本迁移是否向后兼容;不兼容则需先 `migrate down` 到 %d",
		e.Current, e.Expected, e.Expected)
}

// Migrator 封装 golang-migrate 实例。
type Migrator struct {
	m *migrate.Migrate
}

// New 基于内嵌迁移文件与数据库 DSN 构造 Migrator。
//
// DSN 需为 pgx 可识别的 postgres URL。golang-migrate 的 pgx/v5 驱动通过
// schema_migrations 表记录版本,并用 PostgreSQL advisory lock 保证并发安全——
// 多个副本/Job 同时执行时会串行化,不会重复应用同一迁移。
func New(dsn string) (*Migrator, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("加载内嵌迁移文件: %w", err)
	}
	// golang-migrate 用 URL scheme 选择驱动,pgx/v5 驱动注册的是 "pgx5"。
	m, err := migrate.NewWithSourceInstance("iofs", src, pgxDSN(dsn))
	if err != nil {
		return nil, fmt.Errorf("初始化迁移器: %w", err)
	}
	return &Migrator{m: m}, nil
}

// pgxDSN 把 postgres:// 换成 pgx5://,以选中 golang-migrate 的 pgx/v5 驱动。
// 已是 pgx5:// 的原样返回。
func pgxDSN(dsn string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if len(dsn) >= len(prefix) && dsn[:len(prefix)] == prefix {
			return "pgx5://" + dsn[len(prefix):]
		}
	}
	return dsn
}

// Close 释放迁移器持有的连接。
func (mg *Migrator) Close() error {
	serr, derr := mg.m.Close()
	return errors.Join(serr, derr)
}

// Up 应用所有待执行的迁移。已是最新时返回 nil(不视为错误)。
func (mg *Migrator) Up() error {
	if err := mg.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Down 回滚一个版本。刻意**不提供** DownAll:一次性删库不应该只差一条命令。
func (mg *Migrator) Down() error {
	return mg.m.Steps(-1)
}

// Force 把版本表强制标记为指定版本且清除 dirty 标记,不执行任何 SQL。
// 仅用于人工修复失败迁移后重新对齐版本。
func (mg *Migrator) Force(version int) error {
	return mg.m.Force(version)
}

// Version 返回当前 schema 版本与 dirty 标记。空库返回 (0, false, nil)。
func (mg *Migrator) Version() (version uint, dirty bool, err error) {
	v, d, err := mg.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return v, d, err
}

// pgxDriverRegistered 保持对 pgx 驱动包的引用,确保其 init() 注册 "pgx5" scheme。
var _ = pgx.Postgres{}
