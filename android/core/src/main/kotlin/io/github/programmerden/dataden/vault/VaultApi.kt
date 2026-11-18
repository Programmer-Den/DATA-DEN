package io.github.programmerden.dataden.vault

import io.github.programmerden.dataden.sync.DataDenClient
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import okhttp3.MediaType
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okio.BufferedSink
import java.io.InputStream
import java.io.OutputStream

/** One file or folder in the vault, under its sealed name; size is the sealed size. */
@Serializable
data class VaultEntry(val name: String, val dir: Boolean, val size: Long = 0, val modified: String = "")

/**
 * The vault API of the server. Every path is made of sealed names, such as
 * "/<sealed>/<sealed>": the server never sees a plain name or file. Every
 * call blocks; call from a background thread.
 */
class VaultApi(private val client: DataDenClient) {
    /** The wrapped keys; null when the account has no vault yet. */
    fun keys(): VaultKeys? {
        val text = get("api/vault", null) { it.readBytes().decodeToString() }
        return json.decodeFromString<State>(text).keys
    }

    fun list(dir: String): List<VaultEntry> {
        val text = get("api/vault/files", dir) { it.readBytes().decodeToString() }
        return json.decodeFromString<Listing>(text).entries
    }

    fun mkdir(path: String) = post("api/vault/folders", mapOf("path" to path))

    fun move(from: String, to: String) = post("api/vault/move", mapOf("from" to from, "to" to to))

    /** Deletes path for good, a folder with everything in it. */
    fun delete(path: String) = post("api/vault/delete", mapOf("path" to path))

    /** Stores a sealed file of size bytes at path, written by write. */
    fun upload(path: String, size: Long, write: (OutputStream) -> Unit) {
        val body = object : RequestBody() {
            override fun contentType(): MediaType = OCTETS
            override fun contentLength() = size
            override fun isOneShot() = true
            override fun writeTo(sink: BufferedSink) {
                val out = sink.outputStream()
                write(out)
                out.flush()
            }
        }
        val url = client.url("api/vault/file").newBuilder().addQueryParameter("path", path).build()
        client.execute(Request.Builder().url(url).put(body)).use { response ->
            if (!response.isSuccessful) throw client.error(response, response.body.string())
        }
    }

    /** Hands the sealed file at path to read. */
    fun <T> download(path: String, read: (InputStream) -> T): T = get("api/vault/file", path, read)

    private fun <T> get(endpoint: String, path: String?, read: (InputStream) -> T): T {
        val url = client.url(endpoint).newBuilder().apply { if (path != null) addQueryParameter("path", path) }.build()
        client.execute(Request.Builder().url(url).get()).use { response ->
            if (!response.isSuccessful) throw client.error(response, response.body.string())
            return response.body.byteStream().use(read)
        }
    }

    private fun post(endpoint: String, fields: Map<String, String>) {
        client.call(Request.Builder().url(client.url(endpoint)).post(json.encodeToString(fields).toRequestBody(JSON)))
    }

    @Serializable
    private data class State(val keys: VaultKeys? = null)

    @Serializable
    private data class Listing(val entries: List<VaultEntry> = emptyList())

    private companion object {
        val JSON = "application/json".toMediaType()
        val OCTETS = "application/octet-stream".toMediaType()
        val json = Json { ignoreUnknownKeys = true }
    }
}

