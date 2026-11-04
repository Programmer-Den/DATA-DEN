package io.github.programmerden.dataden.sync

import java.time.Instant
import java.time.ZoneId

private const val MAX_PART_BYTES = 255

/**
 * The target folder as typed, such as "Fotos/Handy/", as the server expects
 * it: "/Fotos/Handy". Null when a part is not a name the server accepts.
 */
fun normalizeFolder(input: String): String? {
    val parts = input.trim().trim('/').split('/')
    if (parts.any { !validPart(it) }) return null
    return "/" + parts.joinToString("/")
}

/**
 * Where a photo goes: folder/year/month/name, by the time it was taken in
 * zone, such as "/Fotos/Handy/2026/11/IMG_1234.jpg".
 */
fun targetPath(folder: String, name: String, takenMillis: Long, zone: ZoneId): String {
    val date = Instant.ofEpochMilli(takenMillis).atZone(zone)
    return "%s/%04d/%02d/%s".format(folder, date.year, date.monthValue, safeName(name))
}

/** name made acceptable to the server: no slashes, not hidden, short enough. */
fun safeName(name: String): String {
    var safe = name.replace(Regex("[/\\\\\u0000]"), "_").trim()
    if (safe.isEmpty() || safe == "." || safe == "..") safe = "Datei"
    if (safe.startsWith(".data-den-")) safe = "_$safe"
    if (safe.toByteArray().size <= MAX_PART_BYTES) return safe
    // Shortened, the extension kept.
    val dot = safe.lastIndexOf('.')
    val ext = if (dot > 0 && safe.length - dot <= 16) safe.substring(dot) else ""
    var stem = safe.removeSuffix(ext)
    while ((stem + ext).toByteArray().size > MAX_PART_BYTES) stem = stem.dropLast(1)
    return stem + ext
}

private fun validPart(part: String) = part.isNotEmpty() && part != "." && part != ".." &&
    part.toByteArray().size <= MAX_PART_BYTES && !part.contains('\\') && !part.contains('\u0000') &&
    !part.startsWith(".data-den-")
