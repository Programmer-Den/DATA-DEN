package io.github.programmerden.dataden

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkInfo
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import java.time.Duration

/**
 * Plans the uploads with WorkManager, which keeps them across restarts of
 * the phone:
 *
 * - "watch" waits for a new photo or video in the media store and starts an
 *   upload, then waits again.
 * - "periodic" starts an upload every few hours, for whatever the watch
 *   missed and for uploads that failed.
 * - "upload" runs the uploads one after another, never two at once, when
 *   the network (and charging, if wanted) fit.
 */
object Sync {
    private const val UPLOAD = "upload"
    private const val WATCH = "watch"
    private const val PERIODIC = "periodic"

    /** Blocks; call it off the main thread. */
    fun start(context: Context) {
        val work = WorkManager.getInstance(context)
        work.enqueueUniquePeriodicWork(
            PERIODIC,
            ExistingPeriodicWorkPolicy.UPDATE,
            PeriodicWorkRequestBuilder<KickWorker>(Duration.ofHours(6)).build(),
        )
        watch(context, ExistingWorkPolicy.REPLACE)
        // The settings may have changed: plan the upload with the new ones.
        work.cancelUniqueWork(UPLOAD).result.get()
        kick(context)
    }

    fun stop(context: Context) {
        val work = WorkManager.getInstance(context)
        listOf(PERIODIC, WATCH, UPLOAD).forEach { work.cancelUniqueWork(it) }
    }

    /** Plans an upload, unless one is already waiting to start. Blocks; call it off the main thread. */
    fun kick(context: Context) {
        val work = WorkManager.getInstance(context)
        val waiting = work.getWorkInfosForUniqueWork(UPLOAD).get()
            .any { it.state == WorkInfo.State.ENQUEUED || it.state == WorkInfo.State.BLOCKED }
        if (waiting) return
        val settings = Settings(context)
        val constraints = Constraints.Builder()
            .setRequiredNetworkType(if (settings.wifiOnly) NetworkType.UNMETERED else NetworkType.CONNECTED)
            .setRequiresCharging(settings.chargingOnly)
            .build()
        // Appended, so a new photo during an upload gets its own run after it.
        work.enqueueUniqueWork(
            UPLOAD,
            ExistingWorkPolicy.APPEND_OR_REPLACE,
            OneTimeWorkRequestBuilder<UploadWorker>().setConstraints(constraints).build(),
        )
    }

    /** Waits for a change in the media store; such a wait fires only once. */
    fun watch(context: Context, policy: ExistingWorkPolicy) {
        val constraints = Constraints.Builder()
            // Several photos in a row start one upload.
            .setTriggerContentUpdateDelay(Duration.ofSeconds(10))
            .setTriggerContentMaxDelay(Duration.ofMinutes(2))
        collections(videos = true).forEach { (_, uri) -> constraints.addContentUriTrigger(uri, true) }
        WorkManager.getInstance(context).enqueueUniqueWork(
            WATCH,
            policy,
            OneTimeWorkRequestBuilder<WatchWorker>().setConstraints(constraints.build()).build(),
        )
    }
}

/** Starts an upload, every few hours. */
class KickWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result {
        if (Settings(applicationContext).connected) Sync.kick(applicationContext)
        return Result.success()
    }
}

/** Starts an upload when the media store changed, then waits for the next change. */
class WatchWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    override fun doWork(): Result {
        if (!Settings(applicationContext).connected) return Result.success()
        Sync.kick(applicationContext)
        // Runs after this one has finished.
        Sync.watch(applicationContext, ExistingWorkPolicy.APPEND_OR_REPLACE)
        return Result.success()
    }
}
