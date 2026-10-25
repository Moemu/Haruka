package moe.snowy.haruka.data

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.longPreferencesKey
import androidx.datastore.preferences.core.intPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map

private val Context.dataStore by preferencesDataStore(name = "haruka_settings")

/** 定位方案，与服务端 devices.location_policy 同枚举。 */
const val LOCATION_SERVER = "server"
const val LOCATION_LOCAL = "local"
const val LOCATION_OFF = "off"

data class Settings(
    val serverUrl: String = "",
    val deviceId: String = "",
    val deviceToken: String = "",
    val ownerToken: String = "",
    val heartbeatMin: Int = 15,
    val locationMode: String = LOCATION_SERVER,
    /** 家所在区县全名（如「鹿寨县」）；非空且采样点落入该区时 location 事件附 zone:home。 */
    val homeDistrict: String = "",
    val pausedUntilMs: Long = 0,
    val lastSyncMs: Long = 0,
    val lastActiveMs: Long = 0,
    val manualMood: String = "",
    val manualText: String = "",
    val manualUntilMs: Long = 0,
    val manualCleared: Boolean = true,
    /** 待确认的控制操作：格式 "hide|<untilRfc3339>" 或 "show|"，上线后由服务节拍补发。 */
    val pendingControl: String = "",
) {
    val configured: Boolean
        get() = serverUrl.isNotBlank() && deviceId.isNotBlank() && deviceToken.isNotBlank()

    val baseUrl: String
        get() = serverUrl.trim().trimEnd('/')
}

class SettingsRepository(private val context: Context) {
    private object K {
        val serverUrl = stringPreferencesKey("server_url")
        val deviceId = stringPreferencesKey("device_id")
        val deviceToken = stringPreferencesKey("device_token")
        val ownerToken = stringPreferencesKey("owner_token")
        val heartbeatMin = intPreferencesKey("heartbeat_min")
        val locationMode = stringPreferencesKey("location_mode")
        val homeDistrict = stringPreferencesKey("home_district")
        val pausedUntil = longPreferencesKey("paused_until_ms")
        val lastSync = longPreferencesKey("last_sync_ms")
        val lastActive = longPreferencesKey("last_active_ms")
        val manualMood = stringPreferencesKey("manual_mood")
        val manualText = stringPreferencesKey("manual_text")
        val manualUntil = longPreferencesKey("manual_until_ms")
        val manualCleared = androidx.datastore.preferences.core.booleanPreferencesKey("manual_cleared")
        val pendingControl = stringPreferencesKey("pending_control")
    }

    val settings: Flow<Settings> = context.dataStore.data.map { p ->
        Settings(
            serverUrl = p[K.serverUrl] ?: "",
            deviceId = p[K.deviceId] ?: "",
            deviceToken = p[K.deviceToken] ?: "",
            ownerToken = p[K.ownerToken] ?: "",
            heartbeatMin = p[K.heartbeatMin] ?: 15,
            locationMode = p[K.locationMode] ?: LOCATION_SERVER,
            homeDistrict = p[K.homeDistrict] ?: "",
            pausedUntilMs = p[K.pausedUntil] ?: 0,
            lastSyncMs = p[K.lastSync] ?: 0,
            lastActiveMs = p[K.lastActive] ?: 0,
            manualMood = p[K.manualMood] ?: "",
            manualText = p[K.manualText] ?: "",
            manualUntilMs = p[K.manualUntil] ?: 0,
            manualCleared = p[K.manualCleared] ?: true,
            pendingControl = p[K.pendingControl] ?: "",
        )
    }

    suspend fun current(): Settings = settings.first()

    suspend fun saveCredentials(serverUrl: String, deviceId: String, deviceToken: String, ownerToken: String) {
        context.dataStore.edit {
            it[K.serverUrl] = serverUrl.trim()
            it[K.deviceId] = deviceId.trim()
            it[K.deviceToken] = deviceToken.trim()
            it[K.ownerToken] = ownerToken.trim()
        }
    }

    suspend fun setHeartbeatMin(min: Int) = context.dataStore.edit { it[K.heartbeatMin] = min }

    suspend fun setLocationMode(mode: String) = context.dataStore.edit { it[K.locationMode] = mode }

    suspend fun setHomeDistrict(name: String) = context.dataStore.edit {
        if (name.isBlank()) it.remove(K.homeDistrict) else it[K.homeDistrict] = name.trim()
    }

    suspend fun setPausedUntil(ms: Long) = context.dataStore.edit { it[K.pausedUntil] = ms }

    suspend fun setLastSync(ms: Long) = context.dataStore.edit { it[K.lastSync] = ms }

    suspend fun setLastActive(ms: Long) = context.dataStore.edit { it[K.lastActive] = ms }

    suspend fun setManual(mood: String, text: String, untilMs: Long) = context.dataStore.edit {
        it[K.manualMood] = mood
        it[K.manualText] = text
        it[K.manualUntil] = untilMs
        it[K.manualCleared] = false
    }

    suspend fun setManualCleared() = context.dataStore.edit {
        it[K.manualMood] = ""
        it[K.manualText] = ""
        it[K.manualUntil] = 0
        it[K.manualCleared] = true
    }

    suspend fun setPendingControl(v: String?) = context.dataStore.edit {
        if (v == null) it.remove(K.pendingControl) else it[K.pendingControl] = v
    }
}
