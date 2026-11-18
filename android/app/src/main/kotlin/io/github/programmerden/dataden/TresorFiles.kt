package io.github.programmerden.dataden

import android.content.ContentProvider
import android.content.ContentValues
import android.database.Cursor
import android.database.MatrixCursor
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.OpenableColumns
import android.webkit.MimeTypeMap
import java.io.File
import java.io.FileNotFoundException

/**
 * Hands decrypted copies from the vault to the app that opens them, read
 * only and only to the app the intent grants it to (the provider is not
 * exported). content://<package>.tresor/<folder>/<name> is the file
 * <cache>/tresor/<folder>/<name>; the folder is random, one per copy.
 */
class TresorFiles : ContentProvider() {
    override fun onCreate() = true

    private fun file(uri: Uri): File {
        val segments = uri.pathSegments
        val root = Tresor.dir(context!!).canonicalFile
        if (segments.size != 2) throw FileNotFoundException(uri.toString())
        val file = File(File(root, segments[0]), segments[1]).canonicalFile
        if (file.parentFile?.parentFile != root || !file.isFile) throw FileNotFoundException(uri.toString())
        return file
    }

    override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor {
        if (mode != "r") throw SecurityException("Nur zum Lesen")
        return ParcelFileDescriptor.open(file(uri), ParcelFileDescriptor.MODE_READ_ONLY)
    }

    override fun getType(uri: Uri): String = mimeType(uri.lastPathSegment.orEmpty())

    override fun query(uri: Uri, projection: Array<out String>?, selection: String?, selectionArgs: Array<out String>?, sortOrder: String?): Cursor {
        val file = file(uri)
        val columns = projection ?: arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE)
        val known = columns.filter { it == OpenableColumns.DISPLAY_NAME || it == OpenableColumns.SIZE }
        return MatrixCursor(known.toTypedArray(), 1).apply {
            addRow(known.map { if (it == OpenableColumns.DISPLAY_NAME) file.name else file.length() })
        }
    }

    override fun insert(uri: Uri, values: ContentValues?): Uri? = null

    override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?) = 0

    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?) = 0

    companion object {
        fun authority(packageName: String) = "$packageName.tresor"

        fun uri(packageName: String, file: File): Uri = Uri.Builder().scheme("content").authority(authority(packageName))
            .appendPath(file.parentFile!!.name).appendPath(file.name).build()

        fun mimeType(name: String): String =
            MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "").lowercase())
                ?: "application/octet-stream"
    }
}
