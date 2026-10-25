package moe.snowy.haruka.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.IOException
import java.util.concurrent.TimeUnit

sealed class HttpResult {
    data class Ok(val body: String) : HttpResult()
    data class Http(val code: Int, val body: String) : HttpResult()
    data class Network(val message: String) : HttpResult()
}

@Serializable
data class PublicSnapshot(
    val visibility: String = "visible",
    val online: Boolean = false,
    val hidden_until: String? = null,
    val presence: PresenceView? = null,
    val location: LocationView? = null,
    val activity: ActivityView? = null,
    val mood: MoodView? = null,
    val generated_at: String = "",
) {
    @Serializable
    data class PresenceView(val state: String? = null, val as_of: String? = null)

    @Serializable
    data class LocationView(val display: String? = null, val precision: String? = null, val as_of: String? = null)

    @Serializable
    data class ActivityView(val display: String? = null, val as_of: String? = null)

    @Serializable
    data class MoodView(val mood: String? = null, val text: String? = null, val until: String? = null)
}

/** 服务端 HTTP 客户端：ingest / healthz / 公开快照 / device-policy / control。 */
class ApiClient {
    private val client = OkHttpClient.Builder()
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(15, TimeUnit.SECONDS)
        .writeTimeout(15, TimeUnit.SECONDS)
        .build()

    private val json = Json { ignoreUnknownKeys = true }
    private val jsonMedia = "application/json; charset=utf-8".toMediaType()

    private fun call(request: Request): HttpResult = try {
        client.newCall(request).execute().use { resp ->
            val body = resp.body?.string().orEmpty()
            if (resp.isSuccessful) HttpResult.Ok(body) else HttpResult.Http(resp.code, body)
        }
    } catch (e: IOException) {
        HttpResult.Network(e.message ?: "network error")
    }

    fun ingest(baseUrl: String, token: String, envelopeJson: String): HttpResult =
        call(
            Request.Builder()
                .url("$baseUrl/v1/ingest")
                .header("Authorization", "Bearer $token")
                .post(envelopeJson.toRequestBody(jsonMedia))
                .build(),
        )

    fun healthz(baseUrl: String): HttpResult = call(Request.Builder().url("$baseUrl/healthz").build())

    fun publicStatus(baseUrl: String): HttpResult = call(Request.Builder().url("$baseUrl/v1/public/status").build())

    fun parseSnapshot(body: String): PublicSnapshot? = try {
        json.decodeFromString(PublicSnapshot.serializer(), body)
    } catch (e: Exception) {
        null
    }

    fun devicePolicy(baseUrl: String, token: String, deviceId: String, policy: String): HttpResult {
        val body: JsonObject = buildJsonObject {
            put("device_id", deviceId)
            put("policy", policy)
        }
        return call(
            Request.Builder()
                .url("$baseUrl/v1/device-policy")
                .header("Authorization", "Bearer $token")
                .post(body.toString().toRequestBody(jsonMedia))
                .build(),
        )
    }

    fun control(baseUrl: String, ownerToken: String, action: String, untilRfc3339: String?): HttpResult {
        val body: JsonObject = buildJsonObject {
            put("action", action)
            if (untilRfc3339 != null) put("until", untilRfc3339)
        }
        return call(
            Request.Builder()
                .url("$baseUrl/v1/control")
                .header("Authorization", "Bearer $ownerToken")
                .post(body.toString().toRequestBody(jsonMedia))
                .build(),
        )
    }
}
