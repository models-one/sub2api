package service

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/userplatformquota"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"

	// ent 的字段校验器（含 platform 的 Validate 闭包）由 ent/runtime 的 init 注入，
	// 不 import 它 userplatformquota.PlatformValidator 会是 nil。
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
)

// 平台白名单在本仓库有三个源，任意两处漂移都会变成线上静默故障：
//   1. service.AllowedQuotaPlatforms —— 运行时唯一权威源（后台校验、注册预填都读它）；
//   2. user_platform_quotas.platform 的 DB CHECK —— 终态由迁移
//      229_rename_national_platforms_to_upstream_naming.sql 收敛（230 重建同一份名单）；
//   3. ent/schema/user_platform_quota.go 的 Validate —— 构建期约束，生成到
//      ent/userplatformquota.PlatformValidator。
//
// 漂移的典型后果：后台校验通过 → BulkInsertInitial 那条多行 INSERT 整条撞 DB CHECK 中止
// → 注册路径 fail-open 吞错 → 该用户**一条已配置的默认限额都没落库** = 全平台无限额。
// 这类事故本仓库已经发生过四次（150 / 155 / 157 / 224 各修一次，
// 157 是上游误把白名单收回 5 个平台、158 才恢复）。
//
// ⚠️ 口径变化（上游 0.2.5，迁移 238_purge_unlimited_user_platform_quotas.sql）：
// 「行不存在」现在是**不限额的正常表示**，三档全为 NULL 的行会被主动删除，
// 注册预填也只写至少配了一档限额的平台。所以「用户在 user_platform_quotas 里零条行」
// 从此**不再是**本类事故的可观测特征——排查时别再拿它当信号，
// 真正的特征是「后台明明配了默认限额，却一条都没落库」。本守卫比对的是三侧名单本身，
// 与该口径变化无关，断言继续有效。
//
// migrations 包里的 TestMigration229FinalPlatformWhitelist 只能断言 SQL 里出现了某几个
// 平台名——它把同一份名单硬编码了第二遍，因此挡不住漂移：谁往 AllowedQuotaPlatforms
// 再加一个平台，那边照样绿。这里从三侧的**真源**取值比对。

// 生效的是**最后一个**重建 user_platform_quotas_platform_check 的迁移。
// 早先这里写死 229，但上游每加一个平台就会追加一份新的重建迁移
// （224 → 本 fork 改号 230 → 上游 0.2.4 的 237 加 minimax），
// 写死文件名会让本守卫在每次上游加平台时失效一次。改成按运行器的顺序规则
// （整文件名字典序）自动取最后一个，守卫从此自更新。
func latestPlatformCheckMigrations(t *testing.T) []string {
	t.Helper()

	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err, "读取 migrations 目录失败")

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		content, err := migrations.FS.ReadFile(e.Name())
		require.NoError(t, err, "读取迁移 %s 失败", e.Name())
		if strings.Contains(string(content), "ADD CONSTRAINT user_platform_quotas_platform_check") {
			names = append(names, e.Name())
		}
	}
	require.NotEmpty(t, names, "没有任何迁移重建 user_platform_quotas_platform_check")
	// 迁移运行器按整文件名字典序执行（fs.Glob 后 sort.Strings），这里保持同一规则。
	sort.Strings(names)
	return names
}

func TestQuotaPlatformCheckMatchesAllowedQuotaPlatforms(t *testing.T) {
	chain := latestPlatformCheckMigrations(t)
	final := chain[len(chain)-1]
	sqlPlatforms := parsePlatformCheckWhitelist(t, final)

	goPlatforms := append([]string(nil), AllowedQuotaPlatforms...)
	sort.Strings(goPlatforms)

	require.Equal(t, goPlatforms, sqlPlatforms,
		"AllowedQuotaPlatforms 与最后一个重建 CHECK 的迁移 %s 不一致（重建链：%v）。\n"+
			"新增/删除平台时必须同时追加一个收紧 CHECK 的新迁移——"+
			"已发布迁移受 checksum 保护，不能原地修改。", final, chain)
}

// 重建链上后跑的那份不得把运行时白名单里仍在用的平台**去掉**：
// 去掉就意味着后台校验放行、INSERT 撞 DB CHECK，注册路径 fail-open 吞错后
// 新用户拿到零条配额行 = 全平台无限额（本仓已发生过四次的那类事故）。
// 允许后一份是前一份的超集（上游 237 就是在 230 基础上加 minimax）。
func TestQuotaPlatformCheckNeverDropsAnActivePlatform(t *testing.T) {
	chain := latestPlatformCheckMigrations(t)
	active := make(map[string]struct{}, len(AllowedQuotaPlatforms))
	for _, p := range AllowedQuotaPlatforms {
		active[p] = struct{}{}
	}

	for _, name := range chain {
		got := parsePlatformCheckWhitelist(t, name)
		present := make(map[string]struct{}, len(got))
		for _, p := range got {
			present[p] = struct{}{}
		}
		// 只对链上最后一份做全量相等断言（上面那个用例负责）；
		// 中间各份只要求不比运行时白名单更窄的部分是**有意为之**——
		// 即：凡是它删掉的平台，必须在更靠后的迁移里被重新加回来。
		if name == chain[len(chain)-1] {
			continue
		}
		for p := range active {
			if _, ok := present[p]; ok {
				continue
			}
			// 该平台在这一份里缺席，必须在更靠后的某一份里出现
			found := false
			for _, later := range chain {
				if later <= name {
					continue
				}
				for _, lp := range parsePlatformCheckWhitelist(t, later) {
					if lp == p {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			require.True(t, found,
				"平台 %q 在 AllowedQuotaPlatforms 里，但迁移 %s 的 CHECK 没有它，"+
					"且之后也没有任何迁移把它加回来（重建链：%v）", p, name, chain)
		}
	}
}

// ent 的构建期校验器必须与 AllowedQuotaPlatforms 完全同集：
// 松了就写得进 DB CHECK 拒绝的值，紧了就在 ent 层先炸、错误信息还指不到平台白名单。
func TestEntPlatformValidatorMatchesAllowedQuotaPlatforms(t *testing.T) {
	require.NotNil(t, userplatformquota.PlatformValidator,
		"PlatformValidator 为 nil：ent/runtime 未被 import，校验器没注入")

	for _, p := range AllowedQuotaPlatforms {
		require.NoError(t, userplatformquota.PlatformValidator(p),
			"ent schema 的 Validate 拒绝了 AllowedQuotaPlatforms 里的 %s", p)
	}

	// 已改名 / 已下线的旧标识必须被拒绝，否则它们还能被写进库、
	// 下次收紧 CHECK 的迁移就会在存量行上失败。
	for _, gone := range []string{"moonshot", "glm", "qwen", "seedance"} {
		require.Error(t, userplatformquota.PlatformValidator(gone),
			"ent schema 的 Validate 仍接受已下线平台 %s", gone)
	}
	require.Error(t, userplatformquota.PlatformValidator("not-a-platform"))
}

// AllowedQuotaPlatforms 本身不得有重复项。
// 改名那轮 domain 常量里 PlatformDeepSeek 与 PlatformDeepseek 同为 "deepseek"，
// 两个都留在切片里就会出现重复——注册预填会为 deepseek 写两行，
// 撞 userplatformquota_user_id_platform_uq 部分唯一索引，整条 BulkInsertInitial 中止，
// fail-open 之后新用户一条配额行都没有 = 全平台无限额。
func TestAllowedQuotaPlatformsHasNoDuplicates(t *testing.T) {
	seen := make(map[string]struct{}, len(AllowedQuotaPlatforms))
	for _, p := range AllowedQuotaPlatforms {
		_, dup := seen[p]
		require.False(t, dup, "AllowedQuotaPlatforms 含重复平台 %q", p)
		seen[p] = struct{}{}
	}
}

// parsePlatformCheckWhitelist 从迁移 SQL 里解析
// `ADD CONSTRAINT user_platform_quotas_platform_check ... CHECK (platform IN (...))`
// 的平台列表，返回排序后的切片。
func parsePlatformCheckWhitelist(t *testing.T, migrationName string) []string {
	t.Helper()

	content, err := migrations.FS.ReadFile(migrationName)
	require.NoError(t, err, "读取迁移 %s 失败", migrationName)
	sql := string(content)

	idx := strings.Index(sql, "ADD CONSTRAINT user_platform_quotas_platform_check")
	require.NotEqual(t, -1, idx, "%s 里找不到收紧后的 CHECK", migrationName)

	tail := sql[idx:]
	open := strings.Index(tail, "CHECK (platform IN (")
	require.NotEqual(t, -1, open, "%s 的 CHECK 子句形态与预期不符", migrationName)
	rest := tail[open+len("CHECK (platform IN ("):]
	end := strings.Index(rest, ")")
	require.NotEqual(t, -1, end, "%s 的 CHECK 平台列表没有闭合括号", migrationName)

	matches := regexp.MustCompile(`'([a-z0-9_]+)'`).FindAllStringSubmatch(rest[:end], -1)
	require.NotEmpty(t, matches, "%s 的 CHECK 里没解析出任何平台", migrationName)

	platforms := make([]string, 0, len(matches))
	for _, m := range matches {
		platforms = append(platforms, m[1])
	}
	sort.Strings(platforms)
	return platforms
}
