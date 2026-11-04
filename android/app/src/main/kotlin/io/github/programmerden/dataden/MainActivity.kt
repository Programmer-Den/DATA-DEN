package io.github.programmerden.dataden

import android.Manifest
import android.app.Activity
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.text.format.DateUtils
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.Switch
import android.widget.TextView
import io.github.programmerden.dataden.sync.DataDenClient
import io.github.programmerden.dataden.sync.normalizeFolder
import io.github.programmerden.dataden.sync.serverUrl
import java.io.IOException
import java.util.concurrent.Executors

/** The one screen: connect to the server, choose what to upload, see what happened. */
class MainActivity : Activity() {
    private val background = Executors.newSingleThreadExecutor()
    private lateinit var settings: Settings
    private lateinit var status: Status

    private lateinit var server: EditText
    private lateinit var token: EditText
    private lateinit var folder: EditText
    private lateinit var videos: Switch
    private lateinit var cameraOnly: Switch
    private lateinit var wifiOnly: Switch
    private lateinit var chargingOnly: Switch
    private lateinit var existing: Switch
    private lateinit var save: Button
    private lateinit var message: TextView
    private lateinit var statusText: TextView
    private lateinit var uploadNow: Button
    private lateinit var signOut: Button

    private val statusChanged = SharedPreferences.OnSharedPreferenceChangeListener { _, _ -> showStatus() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        settings = Settings(this)
        status = Status(this)

        server = findViewById(R.id.server)
        token = findViewById(R.id.token)
        folder = findViewById(R.id.folder)
        videos = findViewById(R.id.videos)
        cameraOnly = findViewById(R.id.camera_only)
        wifiOnly = findViewById(R.id.wifi_only)
        chargingOnly = findViewById(R.id.charging_only)
        existing = findViewById(R.id.existing)
        save = findViewById(R.id.save)
        message = findViewById(R.id.message)
        statusText = findViewById(R.id.status)
        uploadNow = findViewById(R.id.upload_now)
        signOut = findViewById(R.id.sign_out)

        if (savedInstanceState == null) {
            server.setText(settings.server)
            folder.setText(settings.folder)
            videos.isChecked = settings.videos
            cameraOnly.isChecked = settings.cameraOnly
            wifiOnly.isChecked = settings.wifiOnly
            chargingOnly.isChecked = settings.chargingOnly
            existing.isChecked = settings.connected && settings.since == 0L
        }
        save.setOnClickListener { connect() }
        uploadNow.setOnClickListener { background.execute { Sync.kick(applicationContext) } }
        signOut.setOnClickListener {
            settings.signOut()
            status.reset()
            background.execute { Sync.stop(applicationContext) }
            message.setText(R.string.signed_out)
            showConnected()
        }
        showConnected()
    }

    override fun onStart() {
        super.onStart()
        status.prefs.registerOnSharedPreferenceChangeListener(statusChanged)
        showStatus()
    }

    override fun onStop() {
        status.prefs.unregisterOnSharedPreferenceChangeListener(statusChanged)
        super.onStop()
    }

    override fun onDestroy() {
        background.shutdown()
        super.onDestroy()
    }

    private fun connect() {
        val address = server.text.toString()
        val target = normalizeFolder(folder.text.toString().ifBlank { Settings.DEFAULT_FOLDER })
        val typed = token.text.toString().trim()
        val secret = typed.ifEmpty { if (settings.server == address) settings.token.orEmpty() else "" }
        val addressError = runCatching { serverUrl(address) }.exceptionOrNull()?.message
        val error = when {
            addressError != null -> addressError
            secret.isEmpty() -> getString(R.string.token_missing)
            target == null -> getString(R.string.folder_invalid)
            else -> null
        }
        if (error != null) {
            message.text = error
            return
        }
        save.isEnabled = false
        message.setText(R.string.checking)
        background.execute {
            val result = try {
                Result.success(DataDenClient(address, secret).username())
            } catch (e: IOException) {
                Result.failure(e)
            }
            runOnUiThread {
                save.isEnabled = true
                result.onSuccess { username -> saved(address, secret, username, target!!) }
                result.onFailure { message.text = getString(R.string.error, it.message ?: it.javaClass.simpleName) }
            }
        }
    }

    private fun saved(address: String, secret: String, username: String, target: String) {
        val newServer = !settings.connected || settings.server != address
        if (newServer) {
            // Another server: what was uploaded to the old one counts for nothing.
            UploadWorker.doneLog(this).clear()
            status.reset()
        }
        settings.server = address
        settings.token = secret
        settings.username = username
        settings.folder = target
        settings.videos = videos.isChecked
        settings.cameraOnly = cameraOnly.isChecked
        settings.wifiOnly = wifiOnly.isChecked
        settings.chargingOnly = chargingOnly.isChecked
        settings.since = when {
            existing.isChecked -> 0
            newServer || settings.since == 0L -> System.currentTimeMillis() / 1000
            else -> settings.since
        }
        token.text.clear()
        folder.setText(target)
        message.text = getString(R.string.connected, username)
        showConnected()
        askPermissions()
    }

    private fun askPermissions() {
        val wanted = buildList {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                add(Manifest.permission.READ_MEDIA_IMAGES)
                add(Manifest.permission.READ_MEDIA_VIDEO)
                add(Manifest.permission.POST_NOTIFICATIONS)
            } else {
                add(Manifest.permission.READ_EXTERNAL_STORAGE)
            }
            add(Manifest.permission.ACCESS_MEDIA_LOCATION)
        }.filter { checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED }
        if (wanted.isEmpty()) {
            startSync()
        } else {
            requestPermissions(wanted.toTypedArray(), REQUEST_PERMISSIONS)
        }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode != REQUEST_PERMISSIONS) return
        val photos = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            Manifest.permission.READ_MEDIA_IMAGES
        } else {
            Manifest.permission.READ_EXTERNAL_STORAGE
        }
        if (checkSelfPermission(photos) != PackageManager.PERMISSION_GRANTED) {
            message.setText(R.string.permission_needed)
        }
        startSync()
    }

    private fun startSync() {
        background.execute { Sync.start(applicationContext) }
    }

    private fun showConnected() {
        val connected = settings.connected
        token.hint = getString(if (connected) R.string.token_saved else R.string.token_hint)
        uploadNow.visibility = if (connected) View.VISIBLE else View.GONE
        signOut.visibility = if (connected) View.VISIBLE else View.GONE
        if (connected && message.text.isEmpty()) message.text = getString(R.string.connected, settings.username)
        if (!connected && message.text.isEmpty()) message.setText(R.string.not_connected)
        showStatus()
    }

    private fun showStatus() {
        if (!settings.connected) {
            statusText.text = ""
            return
        }
        val lastRun = if (status.lastRun == 0L) {
            getString(R.string.status_never)
        } else {
            getString(R.string.status_last_run, DateUtils.getRelativeTimeSpanString(status.lastRun))
        }
        statusText.text = listOf(getString(R.string.status_uploaded, status.uploaded), lastRun, status.message)
            .filter { it.isNotEmpty() }
            .joinToString("\n")
    }

    private companion object {
        const val REQUEST_PERMISSIONS = 1
    }
}
