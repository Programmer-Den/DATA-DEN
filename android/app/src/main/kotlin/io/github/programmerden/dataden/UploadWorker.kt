package io.github.programmerden.dataden

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.util.Log
import androidx.work.ForegroundInfo
import androidx.work.Worker
import androidx.work.WorkerParameters
import io.github.programmerden.dataden.sync.ApiException
import io.github.programmerden.dataden.sync.DataDenClient
import io.github.programmerden.dataden.sync.DoneLog
import io.github.programmerden.dataden.sync.FileChangedException
import io.github.programmerden.dataden.sync.LockedOutException
import io.github.programmerden.dataden.sync.QuotaException
import io.github.programmerden.dataden.sync.UnauthorizedException
import io.github.programmerden.dataden.sync.Uploader
import io.github.programmerden.dataden.sync.targetPath
import java.io.File
import java.io.FileNotFoundException
import java.io.IOException
import java.time.ZoneId

/** Uploads the photos and videos that are not on the server yet. */
class UploadWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    private val settings = Settings(context)
    private val status = Status(context)

    override fun doWork(): Result {
        val token = settings.token
        if (!settings.connected || token == null) return Result.success()
        val done = doneLog(applicationContext)
        val media = try {
            scanMedia(applicationContext, settings).filter { it.key !in done }
        } catch (_: SecurityException) {
            return halt(R.string.problem_permission)
        }
        if (media.isEmpty()) {
            status.finished()
            return Result.success()
        }

        foreground(applicationContext.getString(R.string.notify_scanning), 0, 0)
        val uploader = Uploader(DataDenClient(settings.server, token))
        var note = ""
        for ((index, item) in media.withIndex()) {
            if (isStopped) return Result.success()
            val text = applicationContext.getString(R.string.notify_uploading, index + 1, media.size, item.name)
            foreground(text, 0, 0)
            try {
                val target = targetPath(settings.folder, item.name, item.taken, ZoneId.systemDefault())
                var shown = 0
                uploader.upload(target, item.size, { openMedia(applicationContext, item) }) { sent ->
                    // A notification update per percent is plenty.
                    val percent = (sent * 100 / item.size.coerceAtLeast(1)).toInt()
                    if (percent != shown) {
                        shown = percent
                        foreground(text, percent, 100)
                    }
                }
                done.add(item.key)
                status.countUpload()
            } catch (e: FileChangedException) {
                // Still being written or edited: the next run takes it.
                Log.i(TAG, "skipped ${item.name}: ${e.message}")
            } catch (e: FileNotFoundException) {
                // Deleted since the scan.
                Log.i(TAG, "skipped ${item.name}: ${e.message}")
            } catch (_: SecurityException) {
                return halt(R.string.problem_permission)
            } catch (_: UnauthorizedException) {
                return halt(R.string.problem_unauthorized)
            } catch (_: QuotaException) {
                return halt(R.string.problem_quota)
            } catch (e: LockedOutException) {
                status.finished(e.message.orEmpty())
                return Result.retry()
            } catch (e: ApiException) {
                if (e.status == 403) {
                    // Such as a read-only app password.
                    return halt(applicationContext.getString(R.string.problem_forbidden, e.message.orEmpty()))
                }
                if (e.status >= 500) {
                    status.finished(e.message.orEmpty())
                    return Result.retry()
                }
                // The server refuses this one file, such as its name: go on
                // with the others and try it again next time.
                Log.w(TAG, "refused ${item.name}: ${e.status} ${e.message}")
                note = "${item.name}: ${e.message}"
            } catch (e: IOException) {
                // No connection: WorkManager tries again later.
                status.finished(e.message.orEmpty())
                return Result.retry()
            }
        }
        status.finished(note)
        return Result.success()
    }

    /** Stops until the person fixes the problem in the app. */
    private fun halt(message: Int): Result = halt(applicationContext.getString(message))

    private fun halt(text: String): Result {
        status.finished(text)
        problem(applicationContext, text)
        return Result.failure()
    }

    /** Shows the progress; as a foreground service the upload of a long video may take as long as it needs. */
    private fun foreground(text: String, progress: Int, max: Int) {
        val notification = Notification.Builder(applicationContext, channel(applicationContext, CHANNEL_PROGRESS))
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(applicationContext.getString(R.string.app_name))
            .setContentText(text)
            .setProgress(max, progress, max == 0)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .build()
        try {
            setForegroundAsync(ForegroundInfo(NOTIFY_PROGRESS, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)).get()
        } catch (e: Exception) {
            // Android may refuse a foreground service started in the
            // background; the upload then runs as normal background work.
            Log.i(TAG, "no foreground service: ${e.message}")
        }
    }

    companion object {
        private const val TAG = "DataDen"
        private const val CHANNEL_PROGRESS = "progress"
        private const val CHANNEL_PROBLEMS = "problems"
        private const val NOTIFY_PROGRESS = 1
        private const val NOTIFY_PROBLEM = 2

        fun doneLog(context: Context) = DoneLog(File(context.filesDir, "uploaded.txt"))

        private fun channel(context: Context, id: String): String {
            val manager = context.getSystemService(NotificationManager::class.java)
            val (name, importance) = when (id) {
                CHANNEL_PROGRESS -> R.string.channel_progress to NotificationManager.IMPORTANCE_LOW
                else -> R.string.channel_problems to NotificationManager.IMPORTANCE_DEFAULT
            }
            manager.createNotificationChannel(NotificationChannel(id, context.getString(name), importance))
            return id
        }

        private fun problem(context: Context, text: String) {
            if (context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return
            val open = PendingIntent.getActivity(
                context,
                0,
                Intent(context, MainActivity::class.java),
                PendingIntent.FLAG_IMMUTABLE,
            )
            val notification = Notification.Builder(context, channel(context, CHANNEL_PROBLEMS))
                .setSmallIcon(R.drawable.ic_notification)
                .setContentTitle(context.getString(R.string.problem_title))
                .setContentText(text)
                .setStyle(Notification.BigTextStyle().bigText(text))
                .setContentIntent(open)
                .setAutoCancel(true)
                .build()
            context.getSystemService(NotificationManager::class.java).notify(NOTIFY_PROBLEM, notification)
        }
    }
}
