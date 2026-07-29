package store

// 对真实 PostgreSQL 验证反馈闭环:提升 → 待审 → 审核。

import (
	"context"
	"testing"

	"github.com/aiops/control-plane/internal/model"
)

func gcCleanup(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.pool.Exec(context.Background(),
		`DELETE FROM golden_cases WHERE investigation_id LIKE 'inv-gctest%'`); err != nil {
		t.Fatalf("清理: %v", err)
	}
}

func gcFixture(invID string) (model.Investigation, model.Incident) {
	inv := model.Investigation{
		InvestigationID: invID, TenantID: "default", IncidentID: "inc-gctest",
	}
	inc := model.Incident{
		IncidentID: "inc-gctest", TenantID: "default", ClusterID: "prod-cn-1",
		Version: 2, Status: "resolved", Severity: "P2",
		Title: "checkout 5xx 升高", FaultCategory: "release_regression",
		AffectedResources: []model.ResourceRef{
			{Kind: "Deployment", Name: "checkout", Namespace: "payment"},
		},
		BlastRadius: map[string]any{"services": 1},
	}
	return inv, inc
}

// TestDBPromoteGoldenCaseIsPending 提升出来的用例**必须**是 pending。
//
// 这是反馈闭环最要紧的一条:评测集决定发布质量门槛,一条错误标注的用例会让门槛
// 失真,而这种失真极难发现(门槛照常通过或照常失败,只是标准错了)。
// 自动提升省掉的是"从头写一条用例"的工作量,不是"确认它对不对"的责任。
func TestDBPromoteGoldenCaseIsPending(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	gcCleanup(t, st)
	t.Cleanup(func() { gcCleanup(t, st) })

	inv, inc := gcFixture("inv-gctest-001")
	caseID, created, err := st.PromoteInvestigationToGoldenCase(ctx, inv, inc,
		"连接池大小配置回归导致下游超时", "alice")
	if err != nil {
		t.Fatalf("提升失败: %v", err)
	}
	if !created {
		t.Fatal("首次提升应返回 created=true")
	}

	var status, source, promotedBy string
	if err := st.pool.QueryRow(ctx,
		`SELECT review_status, source, promoted_by FROM golden_cases WHERE case_id=$1`,
		caseID).Scan(&status, &source, &promotedBy); err != nil {
		t.Fatalf("读回: %v", err)
	}
	if status != "pending" {
		t.Errorf("必须是 pending(未经审核不得进评测集), got %q", status)
	}
	if source != SourceHumanFeedback {
		t.Errorf("来源应标记为 %q, got %q", SourceHumanFeedback, source)
	}
	if promotedBy != "alice" {
		t.Errorf("提升人应可追溯, got %q", promotedBy)
	}
}

// TestDBPromoteGoldenCaseIsIdempotent 同一次调查只产出一条用例。
//
// 反馈可以来多次(先 correct 再 confirm),但它们描述同一次故障。
// 不去重会让一次故障在评测集里占多个席位,等于给它加权 ——
// 而评测集的意义在于覆盖多样的故障类型,不是重复计票。
func TestDBPromoteGoldenCaseIsIdempotent(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	gcCleanup(t, st)
	t.Cleanup(func() { gcCleanup(t, st) })

	inv, inc := gcFixture("inv-gctest-002")
	first, created1, err := st.PromoteInvestigationToGoldenCase(ctx, inv, inc, "根因 A", "alice")
	if err != nil || !created1 {
		t.Fatalf("首次: err=%v created=%v", err, created1)
	}
	second, created2, err := st.PromoteInvestigationToGoldenCase(ctx, inv, inc, "根因 B", "bob")
	if err != nil {
		t.Fatalf("二次: %v", err)
	}
	if created2 {
		t.Error("二次提升应返回 created=false")
	}
	if second != first {
		t.Errorf("应返回既有 case_id: %q vs %q", second, first)
	}
	if n := countRows(t, st,
		`SELECT count(*) FROM golden_cases WHERE investigation_id='inv-gctest-002'`); n != 1 {
		t.Errorf("应只有 1 条用例, got %d", n)
	}
}

// TestDBPromoteGoldenCaseRejectsEmptyRootCause 没有标注真值就没有用例。
func TestDBPromoteGoldenCaseRejectsEmptyRootCause(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	inv, inc := gcFixture("inv-gctest-003")
	if _, _, err := st.PromoteInvestigationToGoldenCase(ctx, inv, inc, "   ", "alice"); err == nil {
		t.Error("空根因应被拒绝:没有真值的用例在评测里无法判定对错")
	}
}

// TestDBReviewGoldenCase 审核流转,以及"不可翻转"。
func TestDBReviewGoldenCase(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	gcCleanup(t, st)
	t.Cleanup(func() { gcCleanup(t, st) })

	inv, inc := gcFixture("inv-gctest-004")
	caseID, _, err := st.PromoteInvestigationToGoldenCase(ctx, inv, inc, "连接池耗尽", "alice")
	if err != nil {
		t.Fatalf("提升: %v", err)
	}

	// 待审队列里能看到
	pending, err := st.ListGoldenCases(ctx, "default", "pending", 50)
	if err != nil {
		t.Fatalf("列待审: %v", err)
	}
	found := false
	for _, c := range pending {
		if c.CaseID == caseID {
			found = true
			if len(c.ExpectedCauses) == 0 {
				t.Error("expected_top_causes 不能为空:空期望会让该用例在评测里恒判命中")
			}
		}
	}
	if !found {
		t.Error("新提升的用例应出现在待审队列")
	}

	if err := st.ReviewGoldenCase(ctx, caseID, "approved", "sre-carol", "复核无误"); err != nil {
		t.Fatalf("审核: %v", err)
	}
	var status, reviewer string
	if err := st.pool.QueryRow(ctx,
		`SELECT review_status, reviewed_by FROM golden_cases WHERE case_id=$1`,
		caseID).Scan(&status, &reviewer); err != nil {
		t.Fatalf("读回: %v", err)
	}
	if status != "approved" || reviewer != "sre-carol" {
		t.Errorf("审核结果错误: status=%q reviewer=%q", status, reviewer)
	}

	// 不可翻转:审核是一次决定,反复改会让"评测集当前包含什么"不可知。
	if err := st.ReviewGoldenCase(ctx, caseID, "rejected", "sre-dave", "改判"); err == nil {
		t.Error("已审核的用例不应能再次审核")
	}
}

// TestDBReviewGoldenCaseRejectsBadStatus 只接受 approved / rejected。
func TestDBReviewGoldenCaseRejectsBadStatus(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	for _, bad := range []string{"pending", "maybe", ""} {
		if err := st.ReviewGoldenCase(ctx, "any", bad, "x", ""); err == nil {
			t.Errorf("status=%q 应被拒绝", bad)
		}
	}
}

// TestDBGoldenCaseFixtureIsReplayable fixture 必须含 incident stub 与信号,
// 否则用例无法回放 —— 那它就只是一条备忘,不是评测用例。
func TestDBGoldenCaseFixtureIsReplayable(t *testing.T) {
	st := openStoreDB(t)
	ctx := context.Background()
	gcCleanup(t, st)
	t.Cleanup(func() { gcCleanup(t, st) })

	inv, inc := gcFixture("inv-gctest-005")
	caseID, _, err := st.PromoteInvestigationToGoldenCase(ctx, inv, inc, "磁盘写满", "alice")
	if err != nil {
		t.Fatalf("提升: %v", err)
	}
	var fixture map[string]any
	if err := st.pool.QueryRow(ctx,
		`SELECT signal_fixture FROM golden_cases WHERE case_id=$1`, caseID).Scan(&fixture); err != nil {
		t.Fatalf("读 fixture: %v", err)
	}
	incStub, ok := fixture["incident"].(map[string]any)
	if !ok {
		t.Fatalf("fixture 缺 incident stub: %+v", fixture)
	}
	for _, k := range []string{"severity", "fault_category", "affected_resources"} {
		if _, ok := incStub[k]; !ok {
			t.Errorf("incident stub 缺字段 %q(回放需要它)", k)
		}
	}
	// 不该含易变字段:它们会让回放结果不可复现。
	for _, k := range []string{"updated_at", "last_seen"} {
		if _, ok := incStub[k]; ok {
			t.Errorf("incident stub 不该含易变字段 %q(会让回放不可复现)", k)
		}
	}
	if _, ok := fixture["signals"]; !ok {
		t.Error("fixture 缺 signals(回放输入)")
	}
}

// TestKeywordsFrom 期望关键词的确定性切分。
//
// 刻意不用模型抽取:评测集的期望值必须稳定,否则评测结果会随抽取模型的版本漂移
// —— 那会让"这次发布是否退化"失去意义(分不清是模型退化还是标准变了)。
func TestKeywordsFrom(t *testing.T) {
	got := keywordsFrom("连接池大小配置回归,导致下游超时")
	if len(got) == 0 {
		t.Fatal("不该为空")
	}
	// 确定性:同输入同输出
	for i := 0; i < 5; i++ {
		if again := keywordsFrom("连接池大小配置回归,导致下游超时"); len(again) != len(got) {
			t.Fatal("切分必须是确定性的")
		}
	}
	// 最多 5 个
	many := keywordsFrom("a1 b2 c3 d4 e5 f6 g7 h8")
	if len(many) > 5 {
		t.Errorf("最多 5 个关键词, got %d", len(many))
	}
	// 单字符片段被丢掉(虚词/标点残留)
	for _, k := range keywordsFrom("a bb ccc") {
		if len([]rune(k)) < 2 {
			t.Errorf("不该保留过短片段 %q", k)
		}
	}
	// 兜底:全是短片段时不能返回空 —— 空期望会让用例恒判命中。
	if got := keywordsFrom("a b c"); len(got) == 0 {
		t.Error("全短片段时应兜底为整条根因,不能为空")
	}
}
