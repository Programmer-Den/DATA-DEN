package io.github.programmerden.dataden.sync

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.util.concurrent.TimeUnit

/** An answer of the server other than success, with its German message. */
open class ApiException(
    val status: Int,
    message: String,
    /** The upload's offset, when the server sent one. */
    val offset: Long? = null,
) : IOException(message)

/** The token is wrong, revoked, or its account was deleted. */
class UnauthorizedException(message: String) : ApiException(401, message)

/** Too many wrong tokens from this address; the server locks it out for a while. */
class LockedOutException(message: String, val retryAfterSeconds: Long?) : ApiException(429, message)

/** The account's space is full. */
class QuotaException(message: String) : ApiException(507, message)

@Serializable
data class Entry(val name: String, val dir: Boolean, val size: Long = 0)

data class Started(val id: String, val offset: Long)

/**
 * Talks to a DATA-DEN server with an app password (a token such as
 * "dd_..."), sent as a bearer token. Every call blocks; call from a
 * background thread.
 */
class DataDenClient(
    /** The server's address, ending in a slash; see serverUrl. */
    val base: HttpUrl,
    private val token: String,
    private val http: OkHttpClient = defaultHttp(),
) {
    constructor(server: String, token: String) : this(serverUrl(server), token)

    /** The account the token belongs to; also checks that the token works. */
    fun username(): String {
        val body = call(Request.Builder().url(url("api/session")).get())
        return body.string("username") ?: throw IOException("Unerwartete Antwort des Servers")
    }

    /** The entries of the folder dir, such as "/Fotos". */
    fun list(dir: String): List<Entry> {
        val request = Request.Builder().url(url("api/files").newBuilder().addQueryParameter("path", dir).build()).get()
        execute(request).use { response ->
            val text = response.body.string()
            if (!response.isSuccessful) throw error(response, text)
            return json.decodeFromString<Listing>(text).entries
        }
    }

    /** Announces a new file of size bytes at path, such as "/Fotos/a.jpg". */
    fun startUpload(path: String, size: Long): Started {
        val payload = """{"path":${quote(path)},"size":$size}"""
        val body = call(Request.Builder().url(url("api/uploads")).post(payload.toRequestBody(JSON)))
        val id = body.string("id") ?: throw IOException("Unerwartete Antwort des Servers")
        return Started(id, body.long("offset") ?: 0)
    }

    /** Sends length bytes of chunk at offset; returns the server's new offset. */
    fun putChunk(id: String, offset: Long, chunk: ByteArray, length: Int): Long {
        val request = Request.Builder()
            .url(url("api/uploads").newBuilder().addPathSegment(id).build())
            .header("Upload-Offset", offset.toString())
            .put(chunk.toRequestBody(OCTETS, 0, length))
        return call(request).long("offset") ?: throw IOException("Unerwartete Antwort des Servers")
    }

    /** How much of the upload id the server has, to resume after an error. */
    fun uploadOffset(id: String): Long {
        val request = Request.Builder().url(url("api/uploads").newBuilder().addPathSegment(id).build()).get()
        return call(request).long("offset") ?: throw IOException("Unerwartete Antwort des Servers")
    }

    private fun url(path: String): HttpUrl = base.newBuilder().addPathSegments(path).build()

    private fun execute(request: Request.Builder): Response =
        http.newCall(request.header("Authorization", "Bearer $token").build()).execute()

    private fun call(request: Request.Builder): JsonObject = execute(request).use { response ->
        val text = response.body.string()
        if (!response.isSuccessful) throw error(response, text)
        parse(text) ?: throw IOException("Unerwartete Antwort des Servers")
    }

    private fun error(response: Response, text: String): ApiException {
        val body = parse(text)
        val message = body?.string("error") ?: "Fehler ${response.code}"
        return when (response.code) {
            401 -> UnauthorizedException(message)
            429 -> LockedOutException(message, response.header("Retry-After")?.toLongOrNull())
            507 -> QuotaException(message)
            else -> ApiException(response.code, message, body?.long("offset"))
        }
    }

    @Serializable
    private data class Listing(val entries: List<Entry> = emptyList())

    companion object {
        private val JSON = "application/json".toMediaType()
        private val OCTETS = "application/octet-stream".toMediaType()
        private val json = Json { ignoreUnknownKeys = true }

        fun defaultHttp(): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(30, TimeUnit.SECONDS)
            .readTimeout(2, TimeUnit.MINUTES)
            .writeTimeout(2, TimeUnit.MINUTES)
            .build()

        private fun parse(text: String): JsonObject? =
            runCatching { json.parseToJsonElement(text) as? JsonObject }.getOrNull()

        private fun JsonObject.string(key: String) = this[key]?.jsonPrimitive?.contentOrNull

        private fun JsonObject.long(key: String) = this[key]?.jsonPrimitive?.longOrNull

        private fun quote(s: String) = Json.encodeToString(s)
    }
}

/**
 * The server's address as typed, such as "nas.example.org" or
 * "https://nas.example.org/". Only HTTPS is accepted: the token must never
 * travel unencrypted.
 */
fun serverUrl(input: String): HttpUrl {
    val trimmed = input.trim()
    require(trimmed.isNotEmpty()) { "Bitte die Adresse des Servers eingeben" }
    val withScheme = if ("://" in trimmed) trimmed else "https://$trimmed"
    val url = withScheme.toHttpUrlOrNull()
    require(url != null) { "Ungültige Adresse" }
    require(url.isHttps) { "Nur HTTPS-Adressen sind erlaubt" }
    require(url.username.isEmpty() && url.password.isEmpty() && url.query == null) { "Ungültige Adresse" }
    // A trailing slash keeps a sub path, such as https://host/nas/.
    val segments = url.pathSegments.filter { it.isNotEmpty() }
    return url.newBuilder().encodedPath("/").apply { segments.forEach { addPathSegment(it) } }
        .addPathSegment("").build()
}
