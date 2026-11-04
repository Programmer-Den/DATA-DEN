package io.github.programmerden.dataden

import android.Manifest
import android.content.ContentUris
import android.content.Context
import android.content.pm.PackageManager
import android.net.Uri
import android.provider.MediaStore
import java.io.InputStream

/** A photo or video on the phone. */
data class Media(
    /** Stays the same for this file, such as "images:42". */
    val key: String,
    val uri: Uri,
    val name: String,
    val size: Long,
    /** When it was taken, in milliseconds. */
    val taken: Long,
)

/** The collections searched: photos, and videos when wanted. */
fun collections(videos: Boolean): List<Pair<String, Uri>> = buildList {
    add("images" to MediaStore.Images.Media.getContentUri(MediaStore.VOLUME_EXTERNAL))
    if (videos) add("video" to MediaStore.Video.Media.getContentUri(MediaStore.VOLUME_EXTERNAL))
}

/** The photos and videos to upload, oldest first. Items still being written are left out by the media store. */
fun scanMedia(context: Context, settings: Settings): List<Media> {
    val found = mutableListOf<Media>()
    val projection = arrayOf(
        MediaStore.MediaColumns._ID,
        MediaStore.MediaColumns.DISPLAY_NAME,
        MediaStore.MediaColumns.SIZE,
        MediaStore.MediaColumns.DATE_TAKEN,
        MediaStore.MediaColumns.DATE_ADDED,
    )
    var selection = "${MediaStore.MediaColumns.DATE_ADDED} >= ? AND ${MediaStore.MediaColumns.SIZE} > 0"
    if (settings.cameraOnly) selection += " AND ${MediaStore.MediaColumns.RELATIVE_PATH} LIKE 'DCIM/%'"
    val args = arrayOf(settings.since.toString())
    for ((kind, collection) in collections(settings.videos)) {
        context.contentResolver.query(collection, projection, selection, args, "${MediaStore.MediaColumns.DATE_ADDED} ASC")
            ?.use { c ->
                val id = c.getColumnIndexOrThrow(MediaStore.MediaColumns._ID)
                val name = c.getColumnIndexOrThrow(MediaStore.MediaColumns.DISPLAY_NAME)
                val size = c.getColumnIndexOrThrow(MediaStore.MediaColumns.SIZE)
                val taken = c.getColumnIndexOrThrow(MediaStore.MediaColumns.DATE_TAKEN)
                val added = c.getColumnIndexOrThrow(MediaStore.MediaColumns.DATE_ADDED)
                while (c.moveToNext()) {
                    val rowId = c.getLong(id)
                    found += Media(
                        key = "$kind:$rowId",
                        uri = ContentUris.withAppendedId(collection, rowId),
                        name = c.getString(name) ?: "$rowId",
                        size = c.getLong(size),
                        taken = if (c.isNull(taken) || c.getLong(taken) <= 0) c.getLong(added) * 1000 else c.getLong(taken),
                    )
                }
            }
    }
    return found.sortedBy { it.taken }
}

/**
 * Opens the file as it is on disk. With the location permission that is the
 * original, with the place it was taken; without it Android removes that.
 */
fun openMedia(context: Context, media: Media): InputStream {
    val original = context.checkSelfPermission(Manifest.permission.ACCESS_MEDIA_LOCATION) == PackageManager.PERMISSION_GRANTED
    val uri = if (original) MediaStore.setRequireOriginal(media.uri) else media.uri
    return context.contentResolver.openInputStream(uri) ?: throw java.io.FileNotFoundException(media.uri.toString())
}
