package io.github.programmerden.dataden.vault

import io.github.programmerden.dataden.sync.ApiException
import io.github.programmerden.dataden.sync.DataDenClient
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import mockwebserver3.Dispatcher
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import mockwebserver3.RecordedRequest
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.util.Base64

/** The vault API of the backend (backend/internal/files/vault.go), in memory. */
class FakeVaultServer(private val keys: JsonObject?) : Dispatcher() {
    val server = MockWebServer()

    /** Files by their path of sealed names; folders map to null. */
    val entries = sortedMapOf<String, ByteArray?>()

    fun start(): DataDenClient {
        server.dispatcher = this
        server.start()
        return DataDenClient(server.url("/"), "dd_test")
    }

    override fun dispatch(request: RecordedRequest): MockResponse = synchronized(this) {
        val url = request.url
        val p = url.queryParameter("path")
        when ("${request.method} ${url.encodedPath}") {
            "GET /api/vault" -> json(200, """{"keys":${keys ?: "null"}}""")
            "GET /api/vault/files" -> {
                val prefix = p!!.trimEnd('/') + "/"
                if (prefix != "/" && entries[p] != null) return json(400, """{"error":"Kein Ordner"}""")
                val list = entries.filterKeys { it.startsWith(prefix) && '/' !in it.removePrefix(prefix) }
                    .map { (k, v) -> """{"name":"${k.removePrefix(prefix)}","dir":${v == null},"size":${v?.size ?: 0},"modified":"2026-11-18T19:00:00Z"}""" }
                json(200, """{"entries":[${list.joinToString(",")}]}""")
            }
            "PUT /api/vault/file" -> {
                if (p!! in entries) return json(409, """{"error":"Existiert bereits"}""")
                val body = request.body!!.toByteArray()
                if (body.size.toLong() != request.headers["Content-Length"]?.toLong()) return json(400, """{"error":"Länge"}""")
                if (!body.copyOf(4).contentEquals("DDV1".toByteArray())) return json(400, """{"error":"Keine verschlüsselte Datei"}""")
                entries[p] = body
                json(201, """{"size":${body.size}}""")
            }
            "GET /api/vault/file" -> entries[p!!]?.let {
                MockResponse.Builder().code(200).body(okio.Buffer().write(it)).build()
            } ?: json(404, """{"error":"Nicht gefunden"}""")
            "POST /api/vault/folders" -> {
                val path = field(request, "path")
                if (path in entries) return json(409, """{"error":"Existiert bereits"}""")
                entries[path] = null
                json(201, "{}")
            }
            "POST /api/vault/move" -> {
                val from = field(request, "from")
                val to = field(request, "to")
                for (k in entries.keys.filter { it == from || it.startsWith("$from/") }) {
                    entries[to + k.removePrefix(from)] = entries.remove(k)
                }
                json(200, "{}")
            }
            "POST /api/vault/delete" -> {
                val path = field(request, "path")
                entries.keys.removeAll { it == path || it.startsWith("$path/") }
                json(200, "{}")
            }
            else -> json(404, """{"error":"not found"}""")
        }
    }

    private fun field(request: RecordedRequest, name: String) =
        Json.parseToJsonElement(request.body!!.utf8()).jsonObject[name]!!.jsonPrimitive.content

    private fun json(code: Int, body: String) =
        MockResponse.Builder().code(code).addHeader("Content-Type", "application/json").body(body).build()
}

class VaultSessionTest {
    private val web = webVault()
    private val fake = FakeVaultServer(Json.encodeToJsonElement(VaultKeys.serializer(), web.keys).jsonObject)
    private lateinit var api: VaultApi

    @Before
    fun setUp() {
        api = VaultApi(fake.start())
        // What the browser uploaded, under its sealed name.
        fake.entries["/" + web.names.getValue("Steuer 2025.pdf")] = Base64.getDecoder().decode(web.files.getValue("small").sealed)
    }

    @After
    fun tearDown() {
        fake.server.close()
    }

    private fun session() = VaultSession(api, Vault.open(api.keys()!!, web.passphrase))

    @Test
    fun readsWhatTheBrowserSealed() {
        val session = session()
        val items = session.list()
        assertEquals(listOf("Steuer 2025.pdf"), items.map { it.name })
        assertEquals(39L, items[0].size)
        val out = ByteArrayOutputStream()
        session.download(items[0], out)
        assertArrayEquals(Base64.getDecoder().decode(web.files.getValue("small").plain), out.toByteArray())
    }

    @Test
    fun foldersFilesRenameAndDelete() {
        val session = session()
        session.mkdir("Arzt")
        assertThrows(IOException::class.java) { session.mkdir("Arzt") }
        session.enter(session.list().first { it.name == "Arzt" })
        assertEquals("/Arzt", session.path)

        val content = ByteArray(2 * Vault.CHUNK + 123) { (it % 251).toByte() }
        session.upload("Befund.txt", content.size.toLong(), ByteArrayInputStream(content))
        assertThrows(IOException::class.java) { session.upload("Befund.txt", 1, ByteArrayInputStream(byteArrayOf(1))) }
        session.rename(session.list().single(), "Befund Mai.txt")
        val item = session.list().single()
        assertEquals("Befund Mai.txt", item.name)
        assertEquals(content.size.toLong(), item.size)
        val out = ByteArrayOutputStream()
        session.download(item, out)
        assertArrayEquals(content, out.toByteArray())

        // Nothing readable on the server.
        assertTrue(fake.entries.keys.none { "Befund" in it || "Arzt" in it })

        assertTrue(session.up())
        assertFalse(session.up())
        session.delete(session.list().first { it.name == "Arzt" })
        assertEquals(listOf("Steuer 2025.pdf"), session.list().map { it.name })
    }

    @Test
    fun noVaultYet() {
        val empty = FakeVaultServer(null)
        try {
            assertEquals(null, VaultApi(empty.start()).keys())
        } finally {
            empty.server.close()
        }
    }

    @Test
    fun serverErrorsComeThrough() {
        val session = session()
        val e = assertThrows(ApiException::class.java) {
            session.download(VaultItem("A".repeat(40), "x", false, 1, ""), ByteArrayOutputStream())
        }
        assertEquals(404, e.status)
    }
}
