package moe.snowy.haruka.sync

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import moe.snowy.haruka.HarukaApp
import moe.snowy.haruka.MainActivity
import moe.snowy.haruka.R
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/**
 * 核心常驻：specialUse 类型前台服务，心跳循环 + presence 采样 + 批量上报。
 * 行为守则（spec §2.3）：不持唤醒锁、不精确闹钟、单次心跳秒级完成。
 * 逐场景承诺见 spec §4.1 状态表；不承诺 Doze 内即时同步。
 */
class HeartbeatService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private lateinit var container: HarukaApp.AppContainer
    private lateinit var tracker: ScreenTracker

    override fun onCreate() {
        super.onCreate()
        container = (application as HarukaApp).container
        tracker = ScreenTracker(this, container.settings)
        createChannel()
        tracker.start()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopSelf()
                return START_NOT_STICKY
            }
            else -> {
                startAsForeground(getString(R.string.status_running))
                if (!loopStarted) {
                    loopStarted = true
                    scope.launch { runLoop() }
                }
            }
        }
        return START_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        loopStarted = false
        tracker.stop()
        ServiceRuntime.state.value = ServiceRuntime.state.value.copy(
            running = false,
            phase = RuntimeState.PHASE_STOPPED,
            startedAtMs = 0,
        )
        scope.cancel()
        super.onDestroy()
    }

    /** 先立即打一拍（服务启动即心跳），然后按间隔/退避/手动唤醒等待。 */
    private suspend fun runLoop() {
        ServiceRuntime.state.value = ServiceRuntime.state.value.copy(
            running = true,
            phase = RuntimeState.PHASE_STARTING,
            startedAtMs = System.currentTimeMillis(),
        )
        syncLocationPolicy()
        while (true) {
            // 任何节拍异常都不允许带崩前台服务进程：记日志、按常规间隔重试。
            val extraDelay = try {
                if (!container.settings.current().configured) 0L else doTick()
            } catch (t: Throwable) {
                container.logs.log("tick", "同步异常：${t.javaClass.simpleName}")
                0L
            }
            val wait = if (extraDelay > 0) extraDelay else container.settings.current().heartbeatMin * 60_000L
            withTimeoutOrNull(wait) { flushCh.receive() }
        }
    }

    /** 返回需要额外退避的毫秒数（0 = 无，按常规间隔等待）。 */
    private suspend fun doTick(): Long {
        val settings = container.settings.current()
        val now = System.currentTimeMillis()

        if (settings.pausedUntilMs > now) {
            updateState(RuntimeState.PHASE_PAUSED, settings.lastSyncMs, settings.pausedUntilMs)
            showNotification(getString(R.string.notif_title_paused), formatTime(settings.pausedUntilMs))
            return 0
        }

        val interactive = interactiveNow()
        val keyguardLocked = keyguardLockedNow()
        val outcome = container.engine.tick(
            settings = settings,
            interactive = interactive,
            keyguardLocked = keyguardLocked,
            lastActiveMs = ScreenTracker.lastActiveCache,
            // activity 采集是显式开关（敏感权限），锁屏/灭屏不采样；
            // 电量与步数随拍（READ_STEPS 为 normal 级，非 MIUI 静默缺席）。
            activity = if (settings.activityEnabled) {
                ForegroundApp.sample(this, now, interactive, keyguardLocked)
            } else null,
            device = ForegroundApp.battery(this).copy(steps = ForegroundApp.steps(this) ?: 0),
        )
        val nextTick = System.currentTimeMillis() + settings.heartbeatMin * 60_000L

        if (outcome.ok) {
            container.settings.setLastSync(now)
            updateState(RuntimeState.PHASE_OK, now, nextTick)
            showNotification(
                getString(R.string.notif_title_running),
                getString(R.string.status_next_tick, formatTime(nextTick)),
            )
            flushPendingControl()
            return 0
        }

        val phase = if (outcome.authError) RuntimeState.PHASE_AUTH else RuntimeState.PHASE_WAITING
        updateState(phase, settings.lastSyncMs, System.currentTimeMillis() + outcome.retryDelayMs)
        if (outcome.authError) {
            showNotification(getString(R.string.notif_title_auth), getString(R.string.status_auth_error))
        } else {
            showNotification(
                getString(R.string.notif_title_waiting),
                getString(R.string.status_waiting, outcome.queueLeft),
            )
        }
        return outcome.retryDelayMs
    }

    /**
     * PolicySync（spec §2.2）：启动即把本地定位模式推为服务端策略，失败不阻塞心跳，
     * 服务下次启动重试。模式只在服务端确认后才落盘，正常情况下这是一次幂等同步。
     */
    private suspend fun syncLocationPolicy() {
        try {
            val s = container.settings.current()
            if (!s.configured) return
            val r = container.api.devicePolicy(s.baseUrl, s.deviceToken, s.deviceId, s.locationMode)
            container.logs.log(
                "policy",
                if (r is moe.snowy.haruka.net.HttpResult.Ok) "定位策略已同步：${s.locationMode}"
                else "定位策略同步失败，服务下次启动重试",
            )
        } catch (t: Throwable) {
            container.logs.log("policy", "定位策略同步异常：${t.javaClass.simpleName}")
        }
    }

    private fun updateState(phase: String, lastSyncMs: Long, nextTickMs: Long) {
        ServiceRuntime.state.value = ServiceRuntime.state.value.copy(
            running = true,
            phase = phase,
            lastSyncMs = lastSyncMs,
            nextTickMs = nextTickMs,
            queue = container.spool.count(),
        )
    }

    /** 离线发起的隐身/解除请求上线后补发（spec §6.4「未确认」语义）。 */
    private suspend fun flushPendingControl() {
        val fresh = container.settings.current()
        val pending = fresh.pendingControl
        if (pending.isBlank()) return
        val parts = pending.split("|", limit = 2)
        if (parts.size != 2) {
            container.settings.setPendingControl(null)
            return
        }
        val (action, until) = parts
        if (fresh.ownerToken.isBlank()) {
            container.logs.log("control", "$action 待确认：缺少 owner 令牌")
            return
        }
        val result = container.api.control(fresh.baseUrl, fresh.ownerToken, action, until.ifBlank { null })
        if (result is moe.snowy.haruka.net.HttpResult.Ok) {
            container.settings.setPendingControl(null)
            container.logs.log("control", "$action 已确认")
        } else {
            container.logs.log("control", "$action 仍未确认")
        }
    }

    private fun interactiveNow(): Boolean =
        (getSystemService(Context.POWER_SERVICE) as android.os.PowerManager).isInteractive

    private fun keyguardLockedNow(): Boolean =
        (getSystemService(Context.KEYGUARD_SERVICE) as android.app.KeyguardManager).isKeyguardLocked

    // ---- 通知 ----

    private fun createChannel() {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, getString(R.string.notif_channel_name), NotificationManager.IMPORTANCE_LOW),
        )
    }

    private fun startAsForeground(text: String) {
        val notif = buildNotification(getString(R.string.notif_title_running), text)
        if (Build.VERSION.SDK_INT >= 29) {
            ServiceCompat.startForeground(this, NOTIF_ID, notif, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIF_ID, notif)
        }
    }

    private fun showNotification(title: String, text: String) {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(NOTIF_ID, buildNotification(title, text))
    }

    private fun buildNotification(title: String, text: String): Notification {
        val tap = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_notify)
            .setContentTitle(title)
            .setContentText(text)
            .setOngoing(true)
            .setContentIntent(tap)
            .setOnlyAlertOnce(true)
            .build()
    }

    private fun formatTime(epochMs: Long): String =
        DateTimeFormatter.ofPattern("HH:mm").withZone(ZoneId.systemDefault())
            .format(Instant.ofEpochMilli(epochMs))

    companion object {
        const val ACTION_STOP = "moe.snowy.haruka.STOP"
        private const val CHANNEL_ID = "sync"
        private const val NOTIF_ID = 42

        @Volatile
        private var loopStarted = false

        // 手动发布等操作请求立即同步；CONFLATED 静态通道，服务重建后依然可达。
        val flushCh = Channel<Unit>(Channel.CONFLATED)

        fun start(context: Context) {
            ContextCompat.startForegroundService(
                context,
                Intent(context, HeartbeatService::class.java),
            )
        }

        fun stop(context: Context) {
            context.startService(
                Intent(context, HeartbeatService::class.java).setAction(ACTION_STOP),
            )
        }

        fun requestFlush() {
            flushCh.trySend(Unit)
        }
    }
}
