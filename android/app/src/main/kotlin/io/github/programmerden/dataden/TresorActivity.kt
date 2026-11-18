package io.github.programmerden.dataden

import android.app.Activity
import android.app.AlertDialog
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.provider.DocumentsContract
import android.provider.OpenableColumns
import android.text.format.DateUtils
import android.text.format.Formatter
import android.view.View
import android.view.WindowManager
import android.view.inputmethod.EditorInfo
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.EditText
import android.widget.ListView
import android.widget.TextView
import android.widget.Toast
import io.github.programmerden.dataden.sync.DataDenClient
import io.github.programmerden.dataden.vault.Vault
import io.github.programmerden.dataden.vault.VaultApi
import io.github.programmerden.dataden.vault.VaultItem
import io.github.programmerden.dataden.vault.VaultSession
import io.github.programmerden.dataden.vault.WrongSecretException
import java.io.File
import java.io.IOException
import java.time.Instant
import java.util.UUID

/**
 * The vault (Tresor): opened with the passphrase or recovery key on the
 * phone, files and names encrypted and decrypted here, in the website's
 * format. Kept out of screenshots and the recent apps preview.
 */
class TresorActivity : Activity() {
    private lateinit var locked: View
    private lateinit var opened: View
    private lateinit var secret: EditText
    private lateinit var unlock: Button
    private lateinit var path: TextView
    private lateinit var up: Button
    private lateinit var list: ListView
    private lateinit var message: TextView
    private var items: List<VaultItem> = emptyList()

    /** The item "save as" was asked for, until the file picker answers. */
    private var saving: VaultItem? = null
    private var busy = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
        setContentView(R.layout.activity_tresor)
        setTitle(R.string.tresor_title)
        locked = findViewById(R.id.locked)
        opened = findViewById(R.id.opened)
        secret = findViewById(R.id.secret)
        unlock = findViewById(R.id.unlock)
        path = findViewById(R.id.path)
        up = findViewById(R.id.up)
        list = findViewById(R.id.list)
        message = findViewById(R.id.message)

        unlock.setOnClickListener { unlock() }
        secret.setOnEditorActionListener { _, action, _ ->
            (action == EditorInfo.IME_ACTION_DONE).also { if (it) unlock() }
        }
        up.setOnClickListener { work(null, { it.up() }) { refresh() } }
        findViewById<Button>(R.id.add_file).setOnClickListener {
            pick(Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("*/*"), REQUEST_ADD)
        }
        findViewById<Button>(R.id.add_folder).setOnClickListener {
            askName(getString(R.string.tresor_new_folder), "") { name -> work(name, { it.mkdir(name) }) { refresh() } }
        }
        findViewById<Button>(R.id.lock).setOnClickListener { Tresor.lock() }
        list.setOnItemClickListener { _, _, position, _ -> tap(items[position]) }
        list.setOnItemLongClickListener { _, _, position, _ ->
            actions(items[position])
            true
        }
        if (savedInstanceState == null) Tresor.cleanUp(this)
    }

    override fun onStart() {
        super.onStart()
        Tresor.onLocked = {
            items = emptyList()
            message.setText(R.string.tresor_locked)
            show()
        }
        show()
        if (Tresor.session != null) refresh()
    }

    override fun onStop() {
        Tresor.onLocked = null
        super.onStop()
    }

    @Deprecated("The platform's back handling, as the rest of the app uses Activity")
    override fun onBackPressed() {
        if (Tresor.session != null && up.isEnabled) {
            work(null, { it.up() }) { refresh() }
        } else {
            @Suppress("DEPRECATION")
            super.onBackPressed()
        }
    }

    private fun show() {
        val open = Tresor.session != null
        locked.visibility = if (open) View.GONE else View.VISIBLE
        opened.visibility = if (open) View.VISIBLE else View.GONE
    }

    private fun unlock() {
        val typed = secret.text.toString()
        val settings = Settings(this)
        val token = settings.token
        if (busy || typed.isEmpty()) return
        if (!settings.connected || token == null) {
            message.setText(R.string.not_connected)
            return
        }
        busy = true
        unlock.isEnabled = false
        message.setText(R.string.tresor_unlocking)
        val server = settings.server
        Tresor.worker.execute {
            val result = runCatching {
                val api = VaultApi(DataDenClient(server, token))
                val keys = api.keys() ?: return@runCatching null
                VaultSession(api, Vault.open(keys, typed))
            }
            runOnUiThread {
                busy = false
                unlock.isEnabled = true
                result.onSuccess { session ->
                    if (session == null) {
                        message.setText(R.string.tresor_none)
                    } else {
                        if (isDestroyed) {
                            session.close()
                            return@runOnUiThread
                        }
                        secret.text.clear()
                        message.text = ""
                        Tresor.open(this, session)
                        show()
                        refresh()
                    }
                }
                result.onFailure { failed(it) }
            }
        }
    }

    private fun failed(e: Throwable) {
        message.text = when (e) {
            is WrongSecretException -> getString(R.string.tresor_wrong)
            else -> getString(R.string.error, e.message ?: e.javaClass.simpleName)
        }
    }

    /**
     * Runs task on the vault's thread with what is happening shown, then done
     * on the main thread. Nothing happens when the vault is locked.
     */
    private fun <T> work(what: String?, task: (VaultSession) -> T, done: (T) -> Unit) {
        val session = Tresor.session ?: return
        if (busy) return
        busy = true
        Tresor.touch()
        if (what != null) message.text = getString(R.string.tresor_working, what)
        Tresor.worker.execute {
            val result = runCatching { task(session) }
            runOnUiThread {
                busy = false
                Tresor.touch()
                if (isDestroyed || Tresor.session !== session) return@runOnUiThread
                if (what != null) message.text = ""
                result.onSuccess(done).onFailure { failed(it) }
            }
        }
    }

    private fun refresh() {
        if (Tresor.session == null) return
        if (items.isEmpty()) message.setText(R.string.tresor_loading)
        work(null, { Triple(it.path, it.atTop, it.list()) }) { (where, top, loaded) ->
            path.text = where
            up.isEnabled = !top
            items = loaded
            list.adapter = ArrayAdapter(this, android.R.layout.simple_list_item_1, loaded.map(::label))
            message.text = if (loaded.isEmpty()) getString(R.string.tresor_empty) else ""
        }
    }

    private fun label(item: VaultItem): String {
        if (item.dir) return getString(R.string.tresor_folder_entry, item.label)
        val time = runCatching { Instant.parse(item.modified).toEpochMilli() }.getOrNull()
        val date = time?.let { DateUtils.formatDateTime(this, it, DateUtils.FORMAT_SHOW_DATE or DateUtils.FORMAT_SHOW_TIME) }.orEmpty()
        return getString(R.string.tresor_file_entry, item.label, Formatter.formatShortFileSize(this, item.size), date)
    }

    private fun tap(item: VaultItem) {
        when {
            item.name == null -> actions(item)
            item.dir -> work(null, { it.enter(item) }) { refresh() }
            else -> openCopy(item)
        }
    }

    private fun actions(item: VaultItem) {
        val name = item.name
        val choices = buildList<Pair<Int, () -> Unit>> {
            if (name != null && !item.dir) {
                add(R.string.tresor_open to { openCopy(item) })
                add(R.string.tresor_save_as to {
                    saving = item
                    pick(
                        Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE)
                            .setType(TresorFiles.mimeType(name)).putExtra(Intent.EXTRA_TITLE, name),
                        REQUEST_SAVE,
                    )
                })
            }
            if (name != null) {
                add(R.string.tresor_rename to {
                    askName(getString(R.string.tresor_new_name), name) { to ->
                        work(to, { it.rename(item, to) }) { refresh() }
                    }
                })
            }
            add(R.string.tresor_delete to { confirmDelete(item) })
        }
        AlertDialog.Builder(this)
            .setTitle(item.label)
            .setItems(choices.map { getString(it.first) }.toTypedArray()) { _, which -> choices[which].second() }
            .show()
    }

    private fun confirmDelete(item: VaultItem) {
        AlertDialog.Builder(this)
            .setMessage(getString(R.string.tresor_delete_question, item.label))
            .setPositiveButton(R.string.tresor_delete) { _, _ -> work(item.label, { it.delete(item) }) { refresh() } }
            .setNegativeButton(R.string.tresor_cancel, null)
            .show()
    }

    private fun askName(title: String, current: String, then: (String) -> Unit) {
        val input = EditText(this).apply {
            setText(current)
            setSingleLine()
            setSelection(current.substringBeforeLast('.').length)
        }
        AlertDialog.Builder(this)
            .setTitle(title)
            .setView(input)
            .setPositiveButton(R.string.tresor_ok) { _, _ ->
                val name = input.text.toString().trim()
                if (name.isNotEmpty() && name != current) then(name)
            }
            .setNegativeButton(R.string.tresor_cancel, null)
            .show()
    }

    /** Decrypts item into the cache and hands it to the app that opens its type. */
    private fun openCopy(item: VaultItem) {
        val name = item.name ?: return
        val dir = File(Tresor.dir(this), UUID.randomUUID().toString())
        val file = File(dir, name)
        work(getString(R.string.tresor_decrypting, name), { session ->
            dir.mkdirs()
            try {
                file.outputStream().use { session.download(item, it) }
            } catch (e: Exception) {
                dir.deleteRecursively()
                throw e
            }
        }) {
            val intent = Intent(Intent.ACTION_VIEW)
                .setDataAndType(TresorFiles.uri(packageName, file), TresorFiles.mimeType(name))
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            try {
                startActivity(intent)
                Toast.makeText(this, R.string.tresor_open_note, Toast.LENGTH_LONG).show()
            } catch (_: ActivityNotFoundException) {
                message.setText(R.string.tresor_no_app)
            }
        }
    }

    private fun pick(intent: Intent, request: Int) {
        try {
            startActivityForResult(intent, request)
        } catch (_: ActivityNotFoundException) {
            message.setText(R.string.tresor_no_app)
        }
    }

    @Deprecated("The platform's results, as the rest of the app uses Activity")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        @Suppress("DEPRECATION")
        super.onActivityResult(requestCode, resultCode, data)
        val uri = data?.data
        if (resultCode != RESULT_OK || uri == null) return
        when (requestCode) {
            REQUEST_ADD -> add(uri)
            REQUEST_SAVE -> saving?.let { save(it, uri) }
        }
        saving = null
    }

    /** Encrypts the picked file into the current folder. */
    private fun add(uri: Uri) {
        var name: String? = null
        var size = -1L
        contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
            if (c.moveToFirst()) {
                name = c.getString(0)
                if (!c.isNull(1)) size = c.getLong(1)
            }
        }
        val fileName = name
        if (fileName == null || size < 0) {
            message.text = getString(R.string.error, uri.toString())
            return
        }
        if (size > Vault.MAX_PLAIN) {
            message.setText(R.string.tresor_too_big)
            return
        }
        val resolver = contentResolver
        work(getString(R.string.tresor_encrypting, fileName), { session ->
            val input = resolver.openInputStream(uri) ?: throw IOException(uri.toString())
            input.use { session.upload(fileName, size, it) }
        }) {
            Toast.makeText(this, getString(R.string.tresor_uploaded, fileName), Toast.LENGTH_SHORT).show()
            refresh()
        }
    }

    /** Decrypts item into the document the person picked; a failed one is deleted again. */
    private fun save(item: VaultItem, uri: Uri) {
        val resolver = contentResolver
        work(getString(R.string.tresor_decrypting, item.label), { session ->
            try {
                val out = resolver.openOutputStream(uri, "wt") ?: throw IOException(uri.toString())
                out.use { session.download(item, it) }
            } catch (e: Exception) {
                runCatching { DocumentsContract.deleteDocument(resolver, uri) }
                throw e
            }
        }) {
            Toast.makeText(this, getString(R.string.tresor_saved, item.label), Toast.LENGTH_SHORT).show()
        }
    }

    private companion object {
        const val REQUEST_ADD = 1
        const val REQUEST_SAVE = 2
    }
}
