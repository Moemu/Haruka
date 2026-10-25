package moe.snowy.haruka

import android.app.Application
import android.app.KeyguardManager
import android.content.Context
import android.os.PowerManager
import moe.snowy.haruka.data.LogStore
import moe.snowy.haruka.data.SettingsRepository
import moe.snowy.haruka.data.Spool
import moe.snowy.haruka.location.BoundaryLookup
import moe.snowy.haruka.net.ApiClient
import moe.snowy.haruka.sync.HeartbeatService
import moe.snowy.haruka.sync.ScreenTracker
import moe.snowy.haruka.sync.ServiceRuntime
import moe.snowy.haruka.sync.SyncEngine
import moe.snowy.haruka.sync.SyncWorker

/** 手写 Application 级容器：依赖闭包很小，不引入 DI 框架（spec §2.1）。 */
class HarukaApp : Application() {
    lateinit var container: AppContainer
        private set

    override fun onCreate() {
        super.onCreate()
        container = AppContainer(this)
        SyncWorker.ensureScheduled(this)
    }

    class AppContainer(private val appContext: Context) {
        val settings: SettingsRepository = SettingsRepository(appContext)
        val spool: Spool = Spool(appContext)
        val logs: LogStore = LogStore(appContext)
        val api: ApiClient = ApiClient()
        val engine: SyncEngine = SyncEngine(api, spool, settings, logs)

        /**
         * 边界包反解引擎：源文件是 .gz，但 AGP 打包时自动解压并去掉后缀，
         * APK 内资产名是 haruka-boundaries.json。解压解析较重，首次采样才加载并驻留。
         */
        val lookup: BoundaryLookup by lazy {
            appContext.assets.open("haruka-boundaries.json").use { BoundaryLookup.load(it) }
        }

        /** 服务未运行时的即时同步入口（手动发布/清除后调用）；运行中则唤醒服务提前打拍。 */
        suspend fun syncNowIfIdle(): Boolean = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
            if (ServiceRuntime.state.value.running) {
                HeartbeatService.requestFlush()
                return@withContext true
            }
            val s = settings.current()
            if (!s.configured) return@withContext false
            val pm = appContext.getSystemService(Context.POWER_SERVICE) as PowerManager
            val km = appContext.getSystemService(Context.KEYGUARD_SERVICE) as KeyguardManager
            val outcome = engine.tick(
                settings = s,
                interactive = pm.isInteractive,
                keyguardLocked = km.isKeyguardLocked,
                lastActiveMs = ScreenTracker.lastActiveCache,
            )
            if (outcome.ok) settings.setLastSync(System.currentTimeMillis())
            logs.log("manual", if (outcome.ok) "即时同步成功" else "即时同步失败，已入队")
            outcome.ok
        }
    }
}
