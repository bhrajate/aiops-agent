package migrate

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var migFile = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

// TestExpectedMatchesFiles 保证 Expected 常量与 migrations/ 下的最大迁移号一致。
// 新增迁移却忘记 +1 会在这里失败,而不是在生产启动时被 fail-fast 拦下。
func TestExpectedMatchesFiles(t *testing.T) {
	var maxVer uint
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("读取内嵌迁移目录: %v", err)
	}
	for _, e := range entries {
		m := migFile.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("迁移文件名不符合 golang-migrate 约定 <version>_<name>.(up|down).sql: %s", e.Name())
			continue
		}
		n, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			t.Errorf("无法解析版本号: %s", e.Name())
			continue
		}
		if uint(n) > maxVer {
			maxVer = uint(n)
		}
	}
	if maxVer != Expected {
		t.Errorf("Expected=%d 与迁移文件最大版本 %d 不一致:新增迁移后需同步更新 Expected", Expected, maxVer)
	}
}

// TestEveryUpHasDown 保证每个 up 都有配对的 down。
// 缺 down 的迁移会让"升级失败后回退"这条路径在最需要它的时候不存在。
func TestEveryUpHasDown(t *testing.T) {
	ups, downs := map[string]bool{}, map[string]bool{}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("读取内嵌迁移目录: %v", err)
	}
	for _, e := range entries {
		m := migFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		key := m[1] + "_" + m[2]
		if m[3] == "up" {
			ups[key] = true
		} else {
			downs[key] = true
		}
	}
	if len(ups) == 0 {
		t.Fatal("没有找到任何 up 迁移,go:embed 可能未生效")
	}
	for k := range ups {
		if !downs[k] {
			t.Errorf("迁移 %s 缺少 down 文件:升级失败后无回退路径", k)
		}
	}
	for k := range downs {
		if !ups[k] {
			t.Errorf("迁移 %s 有 down 但无 up", k)
		}
	}
}

// TestVersionsContiguous 保证版本号从 1 起连续。
// golang-migrate 容忍空洞,但空洞会让"当前版本"难以对应到具体变更,
// 且 down 逐步回滚时容易数错步数。
func TestVersionsContiguous(t *testing.T) {
	seen := map[uint]bool{}
	entries, _ := fs.ReadDir(migrationsFS, "migrations")
	for _, e := range entries {
		if m := migFile.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.ParseUint(m[1], 10, 32)
			seen[uint(n)] = true
		}
	}
	for v := uint(1); v <= Expected; v++ {
		if !seen[v] {
			t.Errorf("缺少版本 %d:迁移号应从 1 起连续到 Expected(%d)", v, Expected)
		}
	}
}

func TestPgxDSN(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@h:5432/db?sslmode=disable": "pgx5://u:p@h:5432/db?sslmode=disable",
		"postgresql://u:p@h:5432/db":               "pgx5://u:p@h:5432/db",
		"pgx5://u:p@h:5432/db":                     "pgx5://u:p@h:5432/db",
		"host=localhost user=aiops":                "host=localhost user=aiops",
	}
	for in, want := range cases {
		if got := pgxDSN(in); got != want {
			t.Errorf("pgxDSN(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestErrVersionMismatchMessages 确认三种情形给出的是可操作的指引,
// 而不是只报一个数字——这条错误信息是运维在部署失败时看到的第一手信息。
func TestErrVersionMismatchMessages(t *testing.T) {
	behind := (&ErrVersionMismatch{Current: 2, Expected: 4}).Error()
	if !strings.Contains(behind, "migrate up") {
		t.Errorf("落后时应提示执行迁移,got: %s", behind)
	}
	ahead := (&ErrVersionMismatch{Current: 5, Expected: 4}).Error()
	if !strings.Contains(ahead, "超前") {
		t.Errorf("超前时应说明是回滚镜像场景,got: %s", ahead)
	}
	dirty := (&ErrVersionMismatch{Current: 3, Expected: 4, Dirty: true}).Error()
	if !strings.Contains(dirty, "force") {
		t.Errorf("dirty 时应提示 force 修复,got: %s", dirty)
	}
}
