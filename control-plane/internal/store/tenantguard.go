package store

// 单租户边界的启动护栏。
//
// 设计范围是明确的:"企业私有化、**单租户**、多 Kubernetes 集群",`tenant_id`
// 是**为未来多租户预留**的列(设计文档 §30)。因此租户是**进程级**的
// (由部署决定,`AIOPS_TENANT`),`Principal` 上没有租户字段,读路径也不按它过滤。
//
// 那个决定本身没问题 —— 问题是**没有任何东西阻止你违反它**。
//
// 把两个租户指向同一个数据库,系统会照常跑:signal 落库、incident 聚合、
// 调查启动,而 `GET /v1/incidents` 会把**两个租户的 incident 一起返回**。
// ABAC 拦不住:它按 cluster/namespace 过滤,而两个租户完全可能用同名 namespace
// (payment、cart 这类名字到处都一样)。审计里也看不出异常 —— 每一条都是
// "某个合法用户读了某个存在的 incident"。
//
// 这类错误的代价与发现难度极不对称:配错一个环境变量,而后果是跨租户数据泄漏,
// 且要等到有人注意到"列表里有个我不认识的服务"才会被发现。所以在启动时挡住。

import (
	"context"
	"fmt"
)

// TenantMismatch 描述库里存在与本进程配置不一致的租户数据。
type TenantMismatch struct {
	// Configured 是本进程的 AIOPS_TENANT。
	Configured string
	// Found 是库里实际出现过的租户(含 Configured)。
	Found []string
}

func (e *TenantMismatch) Error() string {
	return fmt.Sprintf(
		"数据库里存在多个租户的数据 %v,而本进程配置的租户是 %q。\n"+
			"  本系统的设计范围是**单租户**部署(tenant_id 为未来多租户预留),"+
			"读路径不按 tenant_id 过滤 —— \n"+
			"  继续启动会让 GET /v1/incidents 把其他租户的 incident 一起返回,"+
			"而 ABAC 拦不住(它按 cluster/namespace 过滤,\n"+
			"  而不同租户完全可能用同名 namespace),审计里也看不出异常。\n"+
			"  处置:为每个租户部署独立的数据库,或确认 AIOPS_TENANT 配置正确。",
		e.Found, e.Configured)
}

// CheckSingleTenant 校验库里只有本进程配置的那个租户的数据。
//
// 只查 incidents:它是所有读路径的入口(investigations/evidence 都挂在它下面),
// 且是唯一会被跨租户列出的表。查全部表只会让启动变慢而不增加信息。
//
// 空库或只有配置租户的数据 → nil。发现其他租户 → *TenantMismatch。
// 查询本身失败 → 返回该错误,由调用方决定是否 fail-fast
// (**不要**把查询失败当成"没问题" —— 那正是这个护栏要防的静默放行)。
func (s *Store) CheckSingleTenant(ctx context.Context, configured string) error {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT tenant_id FROM incidents ORDER BY tenant_id LIMIT 20`)
	if err != nil {
		return fmt.Errorf("检查租户一致性: %w", err)
	}
	defer rows.Close()

	var found []string
	other := false
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return fmt.Errorf("检查租户一致性: %w", err)
		}
		found = append(found, t)
		if t != configured {
			other = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("检查租户一致性: %w", err)
	}
	if other {
		return &TenantMismatch{Configured: configured, Found: found}
	}
	return nil
}
