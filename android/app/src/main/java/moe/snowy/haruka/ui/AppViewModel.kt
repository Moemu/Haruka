package moe.snowy.haruka.ui

import android.Manifest
import android.app.Application
import android.content.Context
import android.content.pm.PackageManager
import android.os.PowerManager
import androidx.core.content.ContextCompat
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch
import moe.snowy.haruka.HarukaApp
import moe.snowy.haruka.R
import moe.snowy.haruka.data.EventRow
import moe.snowy.haruka.data.Settings
import moe.snowy.haruka.data.LOCATION_LOCAL
import moe.snowy.haruka.net.HttpResult
import moe.snowy.haruka.net.PublicSnapshot
import moe.snowy.haruka.protocol.ManualData
import moe.snowy.haruka.protocol.TYPE_MANUAL
import moe.snowy.haruka.protocol.Wire
import moe.snowy.haruka.sync.HeartbeatService
import moe.snowy.haruka.sync.LocationService
import moe.snowy.haruka.sync.ServiceRuntime
import moe.snowy.haruka.sync.SyncWorker

class AppViewModel(app: Application) : AndroidViewModel(app) {
    private val c = (app as HarukaApp).container
    private val appContext = app

    val settings: StateFlow<Settings> = c.settings.settings
        .stateIn(viewModelScope, SharingStarted.Eagerly, Settings())

    val runtime = ServiceRuntime.state

    init {
        // location 型 FGS 不能后台自启：打开应用时恢复定位采样（spec §2.2 已知限制）；
        // 用户主动停止过服务的不恢复（serviceEnabled=false）。
        viewModelScope.launch(Dispatchers.IO) {
            val s = c.settings.current()
            if (s.configured && s.serviceEnabled && s.locationMode == LOCATION_LOCAL && hasLocationPermission()) {
                LocationService.start(appContext)
            }
        }
    }

    private val _snapshot = MutableStateFlow<PublicSnapshot?>(null)
    val snapshot: StateFlow<PublicSnapshot?> = _snapshot

    private val _message = MutableStateFlow<String?>(null)
    val message: StateFlow<String?> = _message

    private val _locSyncing = MutableStateFlow(false)
    val locSyncing: StateFlow<Boolean> = _locSyncing

    // 运行日志走 LogStore（文件），仅诊断页按需读取，不在仪表盘展示。
    private val _diagLogs = MutableStateFlow<List<String>>(emptyList())
    val diagLogs: StateFlow<List<String>> = _diagLogs

    fun loadLogs() {
        viewModelScope.launch(Dispatchers.IO) { _diagLogs.value = c.logs.tail(400) }
    }

    fun clearLogs() {
        viewModelScope.launch(Dispatchers.IO) {
            c.logs.clear()
            _diagLogs.value = emptyList()
        }
    }

    fun consumeMessage() {
        _message.value = null
    }

    // ---- 仪表盘 ----

    fun refreshSnapshot() {
        viewModelScope.launch(Dispatchers.IO) {
            val s = c.settings.current()
            if (!s.configured) return@launch
            _snapshot.value = when (val r = c.api.publicStatus(s.baseUrl)) {
                is HttpResult.Ok -> c.api.parseSnapshot(r.body)
                else -> null
            }
        }
    }


    /** 启动：解除主动停止标记，心跳 + 兜底 + 按当前策略恢复定位采样，一步到位。 */
    fun startService() {
        viewModelScope.launch {
            c.settings.setServiceEnabled(true)
            HeartbeatService.start(appContext)
            SyncWorker.ensureScheduled(appContext)
            val s = c.settings.current()
            if (s.locationMode == LOCATION_LOCAL && hasLocationPermission()) {
                LocationService.start(appContext)
            }
        }
    }

    /**
     * 完全停止：持久化停止标记（SyncWorker 兜底与开机自启据此不再拉起），
     * 取消兜底任务，并连同定位采样一起停——「停止」意味着全部采集停下。
     */
    fun stopService() {
        viewModelScope.launch {
            c.settings.setServiceEnabled(false)
            SyncWorker.cancelScheduled(appContext)
            LocationService.stop(appContext)
            HeartbeatService.stop(appContext)
        }
    }

    /**
     * 定位策略变更（桌面 P1-3 顺序）：先经 /v1/device-policy 征得服务端确认，
     * 成功才落盘并启停定位服务；失败保持原设置（UI 回显确认后的值）。
     * local 前置的定位权限申请由设置页处理。
     */
    fun setLocationMode(mode: String) {
        viewModelScope.launch(Dispatchers.IO) {
            val s = c.settings.current()
            if (!s.configured) return@launch
            _locSyncing.value = true
            when (val r = c.api.devicePolicy(s.baseUrl, s.deviceToken, s.deviceId, mode)) {
                is HttpResult.Ok -> {
                    c.settings.setLocationMode(mode)
                    when (mode) {
                        // 服务处于主动停止状态时不拉起定位采样；启动服务时按策略统一恢复
                        LOCATION_LOCAL ->
                            if (c.settings.current().serviceEnabled) LocationService.start(appContext)
                            else LocationService.stop(appContext)
                        else -> LocationService.stop(appContext)
                    }
                    _message.value = "定位策略已由服务端确认：" + modeLabel(mode)
                }
                is HttpResult.Http -> _message.value = "服务端拒绝（HTTP ${r.code}），保持原设置"
                is HttpResult.Network -> _message.value = "网络不可达，保持原设置"
            }
            _locSyncing.value = false

        }
    }

    fun hasLocationPermission(): Boolean =
        ContextCompat.checkSelfPermission(appContext, Manifest.permission.ACCESS_COARSE_LOCATION) ==
            PackageManager.PERMISSION_GRANTED

    private fun modeLabel(mode: String): String = when (mode) {
        moe.snowy.haruka.data.LOCATION_SERVER -> appContext.getString(R.string.location_mode_server)
        moe.snowy.haruka.data.LOCATION_LOCAL -> appContext.getString(R.string.location_mode_local)
        else -> appContext.getString(R.string.location_mode_off)
    }

    /**
     * 家区名保存（c-3）：先在边界包中校验区名存在，防止拼错导致「在家」永不命中；
     * 留空即清除。
     */
    fun setHomeDistrict(name: String) {
        viewModelScope.launch(Dispatchers.IO) {
            val trimmed = name.trim()
            if (trimmed.isNotEmpty()) {
                val found = runCatching { c.lookup.districts.any { it.name == trimmed } }.getOrDefault(false)
                if (!found) {
                    _message.value = "未找到区县「$trimmed」，请填行政区全名（如：鹿寨县）"
                    return@launch
                }
            }
            c.settings.setHomeDistrict(trimmed)
            _message.value = if (trimmed.isEmpty()) "已清除家区名" else "家区名已保存：$trimmed"
        }
    }

    fun applyHeartbeat(min: Int) {
        viewModelScope.launch { c.settings.setHeartbeatMin(min) }
    }

    // ---- 手动状态 ----

    fun publishManual(mood: String, text: String, untilMs: Long) {
        viewModelScope.launch(Dispatchers.IO) {
            val trimmed = text.trim()
            val data = ManualData(
                mood = mood.ifBlank { null },
                text = trimmed.ifBlank { null },
                until = Wire.rfc3339(untilMs),
            )
            val now = System.currentTimeMillis()
            val ev = Wire.manualEvent(c.spool.nextSeq(), now, data)
            c.spool.enqueue(
                EventRow(
                    id = ev.id, seq = ev.seq, ts = ev.ts, type = TYPE_MANUAL,
                    data = Wire.json.encodeToString(ManualData.serializer(), data),
                ),
            )
            c.settings.setManual(mood, trimmed, untilMs)
            val synced = c.syncNowIfIdle()
            _message.value = if (synced) appContext.getString(R.string.manual_published)
            else appContext.getString(R.string.manual_saved_offline)

        }
    }

    fun clearManual() {
        viewModelScope.launch(Dispatchers.IO) {
            val now = System.currentTimeMillis()
            val data = ManualData(clear = true)
            val ev = Wire.manualEvent(c.spool.nextSeq(), now, data)
            c.spool.enqueue(
                EventRow(
                    id = ev.id, seq = ev.seq, ts = ev.ts, type = TYPE_MANUAL,
                    data = Wire.json.encodeToString(ManualData.serializer(), data),
                ),
            )
            c.settings.setManualCleared()
            val synced = c.syncNowIfIdle()
            _message.value = if (synced) "已清除" else appContext.getString(R.string.manual_saved_offline)

        }
    }

    // ---- 控制操作（暂停 / 隐身） ----

    fun pauseDevice(hours: Int) {
        viewModelScope.launch {
            c.spool.clearAll() // 暂停即清空（设计案 §6.4）
            c.settings.setPausedUntil(System.currentTimeMillis() + hours * 3_600_000L)
            _message.value = "已暂停 $hours 小时，待报队列已清空"

        }
    }

    fun resumeDevice() {
        viewModelScope.launch {
            c.settings.setPausedUntil(0)
            _message.value = "已恢复采集"
        }
    }

    fun control(action: String, untilMs: Long?) {
        viewModelScope.launch(Dispatchers.IO) {
            val s = c.settings.current()
            if (s.ownerToken.isBlank()) {
                _message.value = appContext.getString(R.string.settings_needs_owner)
                return@launch
            }
            val untilRfc3339 = untilMs?.let { Wire.rfc3339(it) }
            when (val r = c.api.control(s.baseUrl, s.ownerToken, action, untilRfc3339)) {
                is HttpResult.Ok -> _message.value = if (action == "hide") "已隐身" else "已解除隐身"
                is HttpResult.Network -> {
                    c.settings.setPendingControl("$action|${untilRfc3339.orEmpty()}")
                    _message.value = "本地已暂停；公开隐藏未确认（等待网络）"
                }
                is HttpResult.Http -> _message.value = "服务端拒绝（HTTP ${r.code}）"
            }

        }
    }

    // ---- 向导 ----

    fun checkServer(url: String, onResult: (Boolean, String) -> Unit) {
        viewModelScope.launch(Dispatchers.IO) {
            val base = url.trim().trimEnd('/')
            if (base.isBlank()) {
                onResult(false, "请输入服务器地址")
                return@launch
            }
            when (val r = c.api.healthz(base)) {
                is HttpResult.Ok -> onResult(true, "服务器可达")
                is HttpResult.Http -> onResult(false, "HTTP ${r.code}")
                is HttpResult.Network -> onResult(false, "无法连接：${r.message}")
            }
        }
    }

    fun verifyCredentials(serverUrl: String, deviceId: String, token: String, onResult: (Boolean, String) -> Unit) {
        viewModelScope.launch(Dispatchers.IO) {
            val base = serverUrl.trim().trimEnd('/')
            if (deviceId.isBlank() || token.isBlank()) {
                onResult(false, "请填写设备 ID 与令牌")
                return@launch
            }
            val envelope = Wire.envelope(deviceId.trim(), snapshot = false, events = listOf(Wire.heartbeatEvent(c.spool.nextSeq(), System.currentTimeMillis())))
            when (val r = c.api.ingest(base, token.trim(), envelope)) {
                is HttpResult.Ok -> onResult(true, "凭据有效")
                is HttpResult.Http -> when (r.code) {
                    401 -> onResult(false, "令牌无效（401）")
                    403 -> onResult(false, "设备 ID 与令牌不匹配（403）")
                    else -> onResult(false, "HTTP ${r.code}")
                }
                is HttpResult.Network -> onResult(false, "无法连接：${r.message}")
            }
        }
    }

    fun completeOnboarding(serverUrl: String, deviceId: String, token: String, ownerToken: String) {
        viewModelScope.launch {
            c.settings.saveCredentials(serverUrl, deviceId, token, ownerToken)
            c.settings.setServiceEnabled(true)
            if (ServiceRuntime.state.value.running) {
                // 服务已随开机/兜底路径启动过：唤醒立即打第一拍。
                HeartbeatService.requestFlush()
            } else {
                HeartbeatService.start(appContext)
            }
            SyncWorker.ensureScheduled(appContext)
        }
    }

    fun batteryWhitelisted(): Boolean {
        val pm = appContext.getSystemService(Context.POWER_SERVICE) as PowerManager
        return pm.isIgnoringBatteryOptimizations(appContext.packageName)
    }

    companion object {
        val Factory = viewModelFactory {
            initializer {
                val app = this[ViewModelProvider.AndroidViewModelFactory.APPLICATION_KEY] as Application
                AppViewModel(app)
            }
        }
    }
}
