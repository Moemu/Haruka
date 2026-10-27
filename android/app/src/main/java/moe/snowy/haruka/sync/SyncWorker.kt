package moe.snowy.haruka.sync

import android.content.Context
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkInfo
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import moe.snowy.haruka.HarukaApp
import java.util.concurrent.TimeUnit

/**
 * 兜底：服务被系统回收后每 15 分钟（平台下限）尝试拉起；拉起被拒时
 * 直接补一发同步（快照规则由 SyncEngine 的 connected 初始态保证）。
 * 属尽力而为，不承诺时限（spec §9.1）。
 */
class SyncWorker(
    context: Context,
    params: WorkerParameters,
) : CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        val c = (applicationContext as HarukaApp).container
        val settings = c.settings.current()
        // 用户主动停止（serviceEnabled=false）与被系统回收不同：兜底不做任何拉起或同步
        if (!settings.configured || !settings.serviceEnabled) return Result.success()

        // 观测回路（spec §5.4）：上次执行被系统停掉的原因入日志，与 doctor 同源排障。
        try {
            val info = WorkManager.getInstance(applicationContext)
                .getWorkInfosForUniqueWork(UNIQUE_NAME).get()
                .firstOrNull { it.id == id }
            val reason = info?.stopReason ?: 0
            if (reason > 0) {
                c.logs.log("worker", "上次执行被系统停止：${stopReasonName(reason)}")
            }
        } catch (_: Exception) {
            // WorkInfo 查询失败不影响兜底同步
        }

        if (ServiceRuntime.state.value.running) return Result.success()

        try {
            HeartbeatService.start(applicationContext)
            c.logs.log("worker", "服务不在运行，已尝试拉起")
            return Result.success()
        } catch (t: Throwable) {
            // 后台启动前台服务被系统限制（ForegroundServiceStartNotAllowedException 等）
            c.logs.log("worker", "服务拉起被拒（${t.javaClass.simpleName}），本轮直接同步")
        }

        val outcome = c.engine.tick(
            settings = settings,
            interactive = false,
            keyguardLocked = false,
            lastActiveMs = ScreenTracker.lastActiveCache,
        )
        if (outcome.ok) c.settings.setLastSync(System.currentTimeMillis())
        c.logs.log("worker", if (outcome.ok) "兜底同步成功" else "兜底同步失败（退避中）")
        return Result.success()
    }

    companion object {
        const val UNIQUE_NAME = "haruka_sync"

        /** WorkManager 的 StopReason 帮助类是 internal，只映射观测中最相关的几类。 */
        fun stopReasonName(reason: Int): String = when (reason) {
            WorkInfo.STOP_REASON_BACKGROUND_RESTRICTION -> "后台限制"
            WorkInfo.STOP_REASON_DEVICE_STATE -> "设备状态（省电/待机）"
            WorkInfo.STOP_REASON_TIMEOUT -> "执行超时"
            WorkInfo.STOP_REASON_QUOTA -> "配额限制"
            WorkInfo.STOP_REASON_APP_STANDBY -> "应用待机"
            WorkInfo.STOP_REASON_PREEMPT -> "被更高优先级抢占"
            else -> "stopReason=$reason"
        }

        fun ensureScheduled(context: Context) {
            val request = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES).build()
            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                UNIQUE_NAME,
                ExistingPeriodicWorkPolicy.KEEP,
                request,
            )
        }

        /** 用户主动停止时随服务一起取消，不再每 15 分钟唤醒进程。 */
        fun cancelScheduled(context: Context) {
            WorkManager.getInstance(context).cancelUniqueWork(UNIQUE_NAME)
        }
    }
}
