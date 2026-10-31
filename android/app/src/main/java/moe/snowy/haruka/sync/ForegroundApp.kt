package moe.snowy.haruka.sync

import android.app.AppOpsManager
import android.app.usage.UsageEvents
import android.app.usage.UsageStatsManager
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.os.BatteryManager
import android.os.Process
import moe.snowy.haruka.protocol.ActivityData
import moe.snowy.haruka.protocol.DeviceData

/**
 * 前台应用与电量采样（spec §6 activity 预留的实现）：
 * - 前台应用：UsageStatsManager 事件流取最近一次 ACTIVITY_RESUMED（锁屏/亮屏不采样，
 *   无「使用情况访问权限」返回 null），包名经内置映射表或 PackageManager 显示名转展示名。
 * - 电量：系统粘性广播 ACTION_BATTERY_CHANGED，无需任何权限。
 * - 步数：MIUI 计步 provider（content://com.miui.providers.steps/item，当日累计，
 *   5 分钟粒度）；READ_STEPS 为 normal 级装即授，实测无需 ACTIVITY_RECOGNITION。
 *   非 MIUI ROM / provider 缺席 / 任何异常一律 null（字段省略）。
 */
object ForegroundApp {

    /** 内置包名映射：展示名（null = 用 PackageManager 标签）到分类。覆盖常见国内应用。 */
    private val knownApps = mapOf(
        "tv.danmaku.bili" to ("B 站" to "media"),
        "com.bilibili.app.in" to ("B 站" to "media"),
        "com.ss.android.ugc.aweme" to ("抖音" to "media"),
        "com.smile.gifmaker" to ("快手" to "media"),
        "com.netease.cloudmusic" to ("网易云音乐" to "music"),
        "com.tencent.qqmusic" to ("QQ音乐" to "music"),
        "com.kugou.android" to ("酷狗音乐" to "music"),
        "cn.kuwo.player" to ("酷我音乐" to "music"),
        "com.tencent.mm" to ("微信" to null),
        "com.tencent.mobileqq" to ("QQ" to null),
        "com.tencent.tim" to ("TIM" to null),
        "com.alibaba.android.rimet" to ("钉钉" to null),
        "com.ss.android.lark" to ("飞书" to null),
        "org.telegram.messenger" to ("Telegram" to null),
        "com.microsoft.emmx" to ("Microsoft Edge" to "browsing"),
        "com.android.chrome" to ("Google Chrome" to "browsing"),
        "org.mozilla.firefox" to ("Firefox" to "browsing"),
        "com.quark.browser" to ("夸克" to "browsing"),
        "com.coolapk.market" to ("酷安" to null),
        // 私密应用：只报「私密应用」，不泄露应用名
        "com.x8bit.bitwarden" to (null to "private"),
        "keepass2android.keepass2android" to (null to "private"),
        "com.agilebits.onepassword" to (null to "private"),
    )

    /** 「使用情况访问权限」是 AppOps 特殊权限，须用户在设置里手动授予。 */
    fun usageAccessGranted(ctx: Context): Boolean {
        val ops = ctx.getSystemService(Context.APP_OPS_SERVICE) as AppOpsManager
        val mode = try {
            ops.unsafeCheckOpNoThrow(
                AppOpsManager.OPSTR_GET_USAGE_STATS, Process.myUid(), ctx.packageName,
            )
        } catch (_: Throwable) {
            AppOpsManager.MODE_DEFAULT
        }
        return mode == AppOpsManager.MODE_ALLOWED
    }

    fun usageAccessSettings(ctx: Context) {
        try {
            ctx.startActivity(
                Intent(android.provider.Settings.ACTION_USAGE_ACCESS_SETTINGS)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            )
        } catch (_: Throwable) {
            // 无该设置页的 ROM：静默放弃，开关仍可开（采样时会因无权限如实空报）
        }
    }

    /**
     * 采样前台应用。锁屏/灭屏不采样；取 24h 窗口内最后一个 RESUMED/PAUSED 事件，
     * 最后一个是 RESUMED 才算有前台应用（停在桌面或刚切走不算）。
     */
    fun sample(ctx: Context, nowMs: Long, interactive: Boolean, keyguardLocked: Boolean): ActivityData? {
        if (!interactive || keyguardLocked || !usageAccessGranted(ctx)) return null
        val usm = ctx.getSystemService(Context.USAGE_STATS_SERVICE) as UsageStatsManager
        val events = usm.queryEvents(nowMs - 24 * 3_600_000L, nowMs)
        val e = UsageEvents.Event()
        var lastPkg: String? = null
        var resumed = false
        while (events.hasNextEvent()) {
            events.getNextEvent(e)
            when (e.eventType) {
                UsageEvents.Event.ACTIVITY_RESUMED -> { lastPkg = e.packageName; resumed = true }
                UsageEvents.Event.ACTIVITY_PAUSED -> { lastPkg = e.packageName; resumed = false }
            }
        }
        val pkg = lastPkg ?: return null
        if (!resumed || pkg == launcherPackage(ctx)) return null

        val (display, category) = knownApps[pkg] ?: (null to null)
        if (category == "private") {
            return ActivityData(kind = "private", category = "private")
        }
        val app = display ?: labelOf(ctx, pkg) ?: return null
        return ActivityData(
            kind = if (category == "browsing") "browser" else "app",
            app = app,
            category = category ?: "other",
        )
    }

    fun battery(ctx: Context): DeviceData {
        val intent = ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val level = intent?.getIntExtra(BatteryManager.EXTRA_LEVEL, -1) ?: -1
        val scale = intent?.getIntExtra(BatteryManager.EXTRA_SCALE, 100) ?: 100
        val status = intent?.getIntExtra(BatteryManager.EXTRA_STATUS, -1) ?: -1
        val pct = if (level >= 0 && scale > 0) level * 100 / scale else 0
        val charging = status == BatteryManager.BATTERY_STATUS_CHARGING ||
            status == BatteryManager.BATTERY_STATUS_FULL
        return DeviceData(batteryLevel = pct.coerceIn(0, 100), charging = charging)
    }

    /**
     * 当日累计步数；来源或权限不满足返回 null（调用方省略 steps 字段）。
     * 查询用 2026-10-31 探针在真机验证过的全列形态——单列 projection 当日实测
     * 静默失败（cursor 无目标列时 sum 恒 0，服务端以 0 为缺席）。首次失败经
     * warn 输出一次诊断，之后静默。
     */
    fun steps(ctx: Context, warn: ((String) -> Unit)? = null): Int? = try {
        val dayStart = java.time.LocalDate.now()
            .atStartOfDay(java.time.ZoneId.systemDefault()).toInstant().toEpochMilli()
        ctx.contentResolver.query(
            android.net.Uri.parse("content://com.miui.providers.steps/item"),
            arrayOf("_begin_time", "_end_time", "_mode", "_steps"),
            "_begin_time >= ?",
            arrayOf(dayStart.toString()),
            null,
        )?.use { c ->
            val iSteps = c.getColumnIndex("_steps")
            var sum = 0L
            while (c.moveToNext()) {
                if (iSteps >= 0) sum += c.getLong(iSteps)
            }
            if (iSteps < 0) warnDiagnostic("cursor 无 _steps 列", warn)
            sum.coerceAtMost(Int.MAX_VALUE.toLong()).toInt()
        }
    } catch (t: Throwable) {
        warnDiagnostic("${t.javaClass.simpleName}: ${t.message.orEmpty().take(120)}", warn)
        null
    }

    @Volatile
    private var stepsDiagnosticLogged = false

    private fun warnDiagnostic(detail: String, warn: ((String) -> Unit)?) {
        if (stepsDiagnosticLogged) return
        stepsDiagnosticLogged = true
        warn?.invoke("步数查询失败：$detail")
    }

    private fun labelOf(ctx: Context, pkg: String): String? = try {
        val pm = ctx.packageManager
        pm.getApplicationLabel(pm.getApplicationInfo(pkg, 0)).toString().take(32)
    } catch (_: Throwable) {
        null // 已卸载或受限包：宁可不报
    }

    private fun launcherPackage(ctx: Context): String? = try {
        ctx.packageManager
            .resolveActivity(Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME), PackageManager.MATCH_DEFAULT_ONLY)
            ?.activityInfo?.packageName
    } catch (_: Throwable) {
        null
    }
}
