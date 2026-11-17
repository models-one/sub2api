//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// 上游 0.1.177（迁移 222-223）的分组用量日汇总曾在同一个事务里先对
// usage_group_rollup_state 单行取 FOR UPDATE，再跑 MIN(created_at) → DELETE 日桶 →
// 对 usage_logs 整段 INSERT..SELECT..GROUP BY，提交才放锁；而迁移 222 给 usage_logs 挂的
// 语句级 AFTER INSERT 触发器每批写入都要对同一行取 FOR KEY SHARE。两者互斥，重建期间网关
// usage_logs 写入全阻塞。本 fork 的计费顺序是 applyUsageBilling（扣余额）在前、
// writeUsageLogBestEffort 在后，写日志预算只有 postUsageBillingTimeout=15s ⇒ 持锁超过
// 15 秒即产生「余额已扣、usage_log 缺失」。故本地把该功能改成按
// dashboard_aggregation.group_usage_rollup_enabled 开关，默认关闭。
//
// 上游 0.2.22（迁移 251）把重建改成非阻塞三段式（登记 building_until / 计算 FOR KEY SHARE /
// 发布 build_dirty_from），并把失效搬到 usage_logs 的行触发器上；清理路径因此不再取
// rollup 行锁（fork 曾经的 cleanupUsageLogsBatchWithRollupInvalidation 已被上游删除）。
// 关闭态下 parkGroupUsageRollupState 把 closed_before 压到 1970，触发器的
// `closed_before > affected_date` 条件恒不成立，usage_logs 的增删改不会产生任何 rollup
// 状态行写入——比原来的"关闭时绕开行锁"更彻底。
//
// 以下三条守卫「关闭时绝不触碰 rollup 状态行/行锁」，第四条守卫「清理路径在开启态也不再取锁」。

func TestDashboardAggregationRepository_GroupUsageRollupDisabled_ParksWatermarkAndSkipsRebuild(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	repo.groupUsageRollupEnabled = false

	// 关闭时只允许发出一条把水位复位到 1970 的条件 UPDATE：
	// 没有 BEGIN、没有 FOR UPDATE、没有对 usage_logs 的聚合。
	// 复位是为了让开→关也安全——读路径的 historical 段 `bucket_date < closed_before`
	// 恒为空，不会拿之前发布过的陈旧日桶算出错的累计金额。
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, repo.SyncGroupUsageRollups(context.Background(),
		time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)))
	// sqlmock 在遇到未预期的语句时会直接报错，因此「没有多余语句」由此断言兜住。
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepository_GroupUsageRollupDisabled_CleanupSkipsRollupRowLock(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	repo.groupUsageRollupEnabled = false

	// 关闭时保留清理走不开事务、不取 rollup 行锁的批删路径。
	// 返回行数 < usageLogsCleanupBatchSize 即停止循环。
	mock.ExpectExec(`DELETE FROM usage_logs`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.cleanupUsageLogsBatches(context.Background(), time.Now().UTC()))
	require.NoError(t, mock.ExpectationsWereMet())
}

// 开启态同样不取 rollup 行锁：上游 0.2.22 的失效由 usage_logs 触发器完成，清理不再需要
// 在批删事务里 FOR UPDATE 那一行（fork 过去的 cleanupUsageLogsBatchWithRollupInvalidation
// 分支已被上游删除）。守卫"开启态不会退回阻塞网关写入的老形状"。
func TestDashboardAggregationRepository_GroupUsageRollupEnabled_CleanupDoesNotTakeRollupRowLock(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db) // 该构造器默认开启
	require.True(t, repo.groupUsageRollupEnabled)

	// 只允许一条批删语句：没有 BEGIN、没有对 usage_group_rollup_state 的 FOR UPDATE。
	mock.ExpectExec(`DELETE FROM usage_logs`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.cleanupUsageLogsBatches(context.Background(), time.Now().UTC()))
	require.NoError(t, mock.ExpectationsWereMet())
}

// 事务内派生的实例必须继承开关；否则 SyncGroupUsageRollups 短路了，
// 但 RecomputeRange 之类在事务里派生出的 txRepo 又会把重建跑起来。
func TestDashboardAggregationRepository_WithSQLInheritsGroupUsageRollupFlag(t *testing.T) {
	db, _ := newSQLMock(t)

	enabled := newDashboardAggregationRepositoryWithSQL(db)
	require.True(t, enabled.withSQL(db).groupUsageRollupEnabled)

	disabled := newDashboardAggregationRepositoryWithSQL(db)
	disabled.groupUsageRollupEnabled = false
	require.False(t, disabled.withSQL(db).groupUsageRollupEnabled)
}

// 关闭态下 CleanupUsageLogs 连 expired_before 都不该推进：日桶永不重建，
// 写 rollup 状态行只会和 usage_logs 触发器的 FOR KEY SHARE 抢行锁。
func TestDashboardAggregationRepository_GroupUsageRollupDisabled_CleanupSkipsRollupStateEntirely(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	repo.groupUsageRollupEnabled = false
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	cutoff := time.Date(2026, 7, 1, 5, 0, 0, 0, time.UTC)

	// 没有 groupRollupExpireExec（不推进 expired_before），只有分区探测 + 批删，
	// 收尾的 SyncGroupUsageRollups 退化成一条把水位复位到 1970 的条件 UPDATE。
	mock.ExpectQuery(`SELECT EXISTS`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`(?s)SELECT tableoid, ctid.*DELETE FROM usage_logs`).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}
