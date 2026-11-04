package io.github.programmerden.dataden.sync

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.long
import mockwebserver3.Dispatcher
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import mockwebserver3.RecordedRequest
import java.io.ByteArrayOutputStream

/** The upload API of the backend (backend/internal/files/upload.go), in memory. */
class FakeServer : Dispatcher() {
    val server = MockWebServer()
    val files = mutableMapOf<String, ByteArray>()
    val token = "dd_test"

    /** PUTs answered 502 after keeping their chunk, like a gateway timeout. */
    var failPuts = 0

    /** PUTs answered 502 without keeping their chunk. */
    var brokenPuts = 0

    /** Forgets all uploads after this many PUTs, like a restart. */
    var restartAfterPuts = -1

    var puts = 0
    private val uploads = mutableMapOf<String, Pending>()
    private var nextId = 1

    private class Pending(val path: String, val size: Long, val data: ByteArrayOutputStream = ByteArrayOutputStream())

    fun start(): DataDenClient {
        server.dispatcher = this
        server.start()
        return DataDenClient(server.url("/"), token)
    }

    override fun dispatch(request: RecordedRequest): MockResponse = synchronized(this) {
        if (request.headers["Authorization"] != "Bearer $token") return json(401, """{"error":"Nicht angemeldet"}""")
        val url = request.url
        val path = url.encodedPath
        when {
            request.method == "GET" && path == "/api/session" -> json(200, """{"username":"anna"}""")
            request.method == "GET" && path == "/api/files" -> list(url.queryParameter("path")!!)
            request.method == "POST" && path == "/api/uploads" -> startUpload(request.body!!.utf8())
            request.method == "PUT" && path.startsWith("/api/uploads/") -> put(path.removePrefix("/api/uploads/"), request)
            request.method == "GET" && path.startsWith("/api/uploads/") -> {
                val u = uploads[path.removePrefix("/api/uploads/")] ?: return json(404, """{"error":"Upload nicht gefunden"}""")
                json(200, """{"offset":${u.data.size()},"size":${u.size}}""")
            }
            else -> json(404, """{"error":"not found"}""")
        }
    }

    private fun list(dir: String): MockResponse {
        val prefix = dir.trimEnd('/') + "/"
        val entries = files.filterKeys { it.startsWith(prefix) && '/' !in it.removePrefix(prefix) }
            .map { (p, data) -> """{"name":${Json.encodeToString(p.removePrefix(prefix))},"dir":false,"size":${data.size}}""" }
        if (entries.isEmpty()) return json(404, """{"error":"Nicht gefunden"}""")
        return json(200, """{"path":"$dir","entries":[${entries.joinToString(",")}]}""")
    }

    private fun startUpload(body: String): MockResponse {
        val obj = Json.parseToJsonElement(body).jsonObject
        val target = obj["path"]!!.jsonPrimitive.content
        val size = obj["size"]!!.jsonPrimitive.long
        if (target in files) return json(409, """{"error":"Datei existiert bereits"}""")
        val id = "u${nextId++}"
        val upload = Pending(target, size)
        if (size == 0L) {
            files[target] = ByteArray(0)
        } else {
            uploads[id] = upload
        }
        return json(201, """{"id":"$id","offset":0}""")
    }

    private fun put(id: String, request: RecordedRequest): MockResponse {
        puts++
        if (puts == restartAfterPuts + 1) uploads.clear()
        val u = uploads[id] ?: return json(404, """{"error":"Upload nicht gefunden"}""")
        if (brokenPuts > 0) {
            brokenPuts--
            return json(502, """{"error":"Bad Gateway"}""")
        }
        val offset = request.headers["Upload-Offset"]!!.toLong()
        if (offset != u.data.size().toLong()) return json(409, """{"error":"Falscher Offset","offset":${u.data.size()}}""")
        val chunk = request.body!!.toByteArray()
        u.data.write(chunk, 0, minOf(chunk.size.toLong(), u.size - offset).toInt())
        if (failPuts > 0) {
            failPuts--
            return json(502, """{"error":"Bad Gateway"}""")
        }
        if (u.data.size().toLong() == u.size) {
            uploads.remove(id)
            if (u.path in files) return json(409, """{"error":"Datei existiert bereits"}""")
            files[u.path] = u.data.toByteArray()
            return json(200, """{"offset":${u.size},"duplicates":[]}""")
        }
        return json(200, """{"offset":${u.data.size()}}""")
    }

    private fun json(code: Int, body: String) = MockResponse.Builder()
        .code(code)
        .addHeader("Content-Type", "application/json")
        .body(body)
        .build()
}
