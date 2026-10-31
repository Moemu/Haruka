package moe.snowy.haruka.sync

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.location.Location
import android.location.LocationManager
import android.os.Build
import android.os.CancellationSignal
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeoutOrNull
import moe.snowy.haruka.HarukaApp
import moe.snowy.haruka.MainActivity
import moe.snowy.haruka.R
import moe.snowy.haruka.data.EventRow
import moe.snowy.haruka.data.LOCATION_LOCAL
import moe.snowy.haruka.location.BoundaryLookup
import moe.snowy.haruka.location.locationData
import moe.snowy.haruka.protocol.LocationData
import moe.snowy.haruka.protocol.Wire
import moe.snowy.haruka.protocol.ZONE_HOME
import moe.snowy.haruka.protocol.ZONE_SCHOOL
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.resume

/**
 * 方案 B 采集服务（location 型 FGS，spec §2.2）：低频采样（2 小时 + 亮屏触发），
 * 坐标反解为行政区名后立即入队。行为守则（§2.3）：即取即弃，不注册持续监听、
 * 不持唤醒锁；坐标不出本服务内存、不落盘不日志（同步日志只记区名）。
 * 仅在用户前台确认策略后启动；系统回收后需打开应用恢复（location 型 FGS
 * 不能后台自启，设置页如实展示），故 START_NOT_STICKY。
 */
class LocationService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private lateinit var container: HarukaApp.AppContainer

    private val screenOnReceiver = object : BroadcastReceiver() {
        override fun onReceive(ctx: Context, intent: Intent) {
            sampleCh.trySend(Unit)
        }
    }

    override fun onCreate() {
        super.onCreate()
        container = (application as HarukaApp).container
        createChannel()
        val filter = IntentFilter(Intent.ACTION_SCREEN_ON)
        if (Build.VERSION.SDK_INT >= 33) {
            registerReceiver(screenOnReceiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            ContextCompat.registerReceiver(this, screenOnReceiver, filter, ContextCompat.RECEIVER_NOT_EXPORTED)
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopSelf()
                return START_NOT_STICKY
            }
            else -> {
                startAsForeground()
                if (!loopStarted) {
                    loopStarted = true
                    scope.launch { runLoop() }
                }
            }
        }
        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        loopStarted = false
        try {
            unregisterReceiver(screenOnReceiver)
        } catch (_: IllegalArgumentException) {
            // 未注册或已注销
        }
        scope.cancel()
        super.onDestroy()
    }

    /** 先立即采样一发（用户刚开启，尽快见效），之后 2 小时或亮屏触发。 */
    private suspend fun runLoop() {
        while (true) {
            try {
                sampleOnce()
            } catch (t: Throwable) {
                container.logs.log("location", "采样异常：${t.javaClass.simpleName}")
            }
            withTimeoutOrNull(SAMPLE_INTERVAL_MS) { sampleCh.receive() }
        }
    }

    private suspend fun sampleOnce() {
        val s = container.settings.current()
        val now = System.currentTimeMillis()
        // 策略闸门：locationMode 只在服务端确认后才落盘；暂停期间不采样。
        if (!s.configured || s.locationMode != LOCATION_LOCAL || s.pausedUntilMs > now) return
        val lm = getSystemService(Context.LOCATION_SERVICE) as LocationManager
        if (!lm.isLocationEnabled) {
            container.logs.log("location", "系统定位已关闭，跳过采样")
            return
        }
        val fix = awaitFix(lm)
        if (fix == null) {
            container.logs.log("location", "未取得定位（provider 无结果）")
            return
        }
        val data: LocationData? = locationData(container.lookup, fix.latitude, fix.longitude, s.homeDistrict, s.schoolDistrict)
        if (data == null) {
            container.logs.log("location", "坐标未落中国边界内，保持静默")
            return
        }
        val ev = Wire.locationEvent(container.spool.nextSeq(), now, data)
        container.spool.enqueue(
            EventRow(
                id = ev.id,
                seq = ev.seq,
                ts = ev.ts,
                type = ev.type,
                data = Wire.json.encodeToString(LocationData.serializer(), data),
            ),
        )
        val place = listOfNotNull(data.province, data.city, data.district).joinToString(" ")
        val atZone = when (data.zone) {
            ZONE_HOME -> "（在家）"
            ZONE_SCHOOL -> "（在学校）"
            else -> ""
        }
        container.logs.log("location", "已采样：$place（${data.precision}）$atZone")
        HeartbeatService.requestFlush()
    }

    /** NETWORK 与 GPS 竞速取最先到达的定位；都无结果再试 PASSIVE（可能为旧值，仅兜底）。 */
    private suspend fun awaitFix(lm: LocationManager): Location? {
        val providers = listOf(LocationManager.NETWORK_PROVIDER, LocationManager.GPS_PROVIDER)
            .filter { runCatching { lm.isProviderEnabled(it) }.getOrDefault(false) }
        val raced = if (providers.isEmpty()) null else coroutineScope {
            val ch = Channel<Location>(Channel.CONFLATED)
            val jobs = providers.map { p -> launch { oneShot(lm, p)?.let { ch.trySend(it) } } }
            val fix = withTimeoutOrNull(FIX_TIMEOUT_MS) { ch.receive() }
            jobs.forEach { it.cancel() }
            fix
        }
        return raced ?: withTimeoutOrNull(PASSIVE_TIMEOUT_MS) { oneShot(lm, LocationManager.PASSIVE_PROVIDER) }
    }

    /** 单次取位（即取即弃）：API 30+ 用 getCurrentLocation，API 29 用 requestSingleUpdate。 */
    private suspend fun oneShot(lm: LocationManager, provider: String): Location? =
        suspendCancellableCoroutine { cont ->
            val done = AtomicBoolean(false)
            fun finish(v: Location?) {
                if (done.compareAndSet(false, true)) cont.resume(v)
            }
            if (Build.VERSION.SDK_INT >= 30) {
                val signal = CancellationSignal()
                cont.invokeOnCancellation { signal.cancel() }
                try {
                    lm.getCurrentLocation(provider, signal, mainExecutor) { loc -> finish(loc) }
                } catch (t: Throwable) {
                    // SecurityException（权限被撤销）或 provider 不可用
                    finish(null)
                }
            } else {
                @Suppress("DEPRECATION")
                val listener = android.location.LocationListener { loc -> finish(loc) }
                cont.invokeOnCancellation { lm.removeUpdates(listener) }
                try {
                    lm.requestSingleUpdate(provider, listener, mainLooper)
                } catch (t: Throwable) {
                    finish(null)
                }
            }
        }

    // ---- 通知 ----

    private fun createChannel() {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, getString(R.string.location_channel_name), NotificationManager.IMPORTANCE_LOW),
        )
    }

    private fun startAsForeground() {
        ServiceCompat.startForeground(this, NOTIF_ID, buildNotification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_LOCATION)
    }

    private fun buildNotification(): Notification {
        val tap = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_notify)
            .setContentTitle(getString(R.string.notif_title_location))
            .setContentText(getString(R.string.location_restore_note))
            .setOngoing(true)
            .setContentIntent(tap)
            .setOnlyAlertOnce(true)
            .build()
    }

    companion object {
        const val ACTION_STOP = "moe.snowy.haruka.LOCATION_STOP"
        private const val CHANNEL_ID = "location"
        private const val NOTIF_ID = 43

        /** 采样间隔：默认 2 小时，另由亮屏触发（spec §2.2）。 */
        const val SAMPLE_INTERVAL_MS = 2L * 60 * 60 * 1000
        private const val FIX_TIMEOUT_MS = 30_000L
        private const val PASSIVE_TIMEOUT_MS = 10_000L

        @Volatile
        private var loopStarted = false

        // 亮屏触发的采样请求；CONFLATED 静态通道，服务重建后依然可达。
        val sampleCh = Channel<Unit>(Channel.CONFLATED)

        fun start(context: Context) {
            ContextCompat.startForegroundService(
                context,
                Intent(context, LocationService::class.java),
            )
        }

        fun stop(context: Context) {
            context.startService(
                Intent(context, LocationService::class.java).setAction(ACTION_STOP),
            )
        }
    }
}
