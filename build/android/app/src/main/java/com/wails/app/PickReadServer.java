package com.wails.app;

import android.content.ContentResolver;
import android.content.Context;
import android.database.Cursor;
import android.net.Uri;
import android.provider.OpenableColumns;
import android.util.Log;

import java.io.BufferedOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.ByteBuffer;
import java.nio.channels.FileChannel;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * Serves the {@code content://} URIs returned by the document picker over
 * loopback HTTP so the Go uploader can read them with random access and Range
 * requests.
 *
 * <p>The previous flow copied every picked file into the app cache before Go saw
 * a path, which doubled the bytes moved and delayed the first byte until the
 * whole batch had been duplicated. Reading in place removes both costs.
 *
 * <p>Security: every app on the device can reach 127.0.0.1, so the per-file
 * 256-bit token in the path <em>is</em> the access control. The socket is bound
 * to the loopback interface only, the {@code Host} header must match this
 * server's own authority, and only GET/HEAD are served. Tokens are never logged.
 *
 * <p>This is a process singleton rather than an Activity field so an in-flight
 * transfer survives a WebView reload.
 */
public final class PickReadServer {
    private static final String TAG = "PickReadServer";

    /**
     * Covers the worst case of concurrent ranged reads: the uploader runs
     * maxParallelUploads files at once, each split into maxSegmentsPerFile
     * ranges. Go caps MaxConnsPerHost to this same number so a burst queues here
     * instead of piling up half-open sockets the backlog would drop.
     */
    private static final int MAX_WORKERS = 16;
    private static final int BACKLOG = 64;
    private static final int COPY_CHUNK = 8 << 20;

    private static volatile PickReadServer instance;

    private final Context context;
    private final SecureRandom random = new SecureRandom();
    private final Map<String, Entry> entries = new ConcurrentHashMap<>();
    private final ExecutorService workers = Executors.newFixedThreadPool(MAX_WORKERS);

    private volatile ServerSocket socket;
    private volatile Thread acceptor;
    private volatile boolean running;

    private PickReadServer(Context context) {
        this.context = context.getApplicationContext();
    }

    public static synchronized PickReadServer get(Context context) {
        if (instance == null) {
            instance = new PickReadServer(context);
        }
        instance.start();
        return instance;
    }

    private synchronized void start() {
        if (running) {
            return;
        }
        try {
            ServerSocket s = new ServerSocket(0, BACKLOG, InetAddress.getByName("127.0.0.1"));
            s.setReuseAddress(true);
            socket = s;
            running = true;
            acceptor = new Thread(this::acceptLoop, "pick-read-accept");
            acceptor.setDaemon(true);
            acceptor.start();
        } catch (IOException e) {
            Log.e(TAG, "failed to start read server", e);
            running = false;
        }
    }

    private int port() {
        ServerSocket s = socket;
        return s == null ? -1 : s.getLocalPort();
    }

    private void acceptLoop() {
        while (running) {
            ServerSocket s = socket;
            if (s == null) {
                return;
            }
            try {
                Socket client = s.accept();
                workers.execute(() -> handle(client));
            } catch (IOException e) {
                if (running) {
                    Log.w(TAG, "accept failed", e);
                }
                return;
            } catch (java.util.concurrent.RejectedExecutionException e) {
                return;
            }
        }
    }

    /**
     * Registers a picked document and returns the source string the Go uploader
     * should open: normally a {@code lightpick://} URL, or an absolute cache path
     * when the provider is not seekable and the file had to be spooled.
     */
    public Picked register(Uri uri) {
        if (port() < 0) {
            // The socket failed to bind; emitting lightpick://127.0.0.1:-1/...
            // would hand the uploader an unusable URL.
            Log.e(TAG, "read server is not listening; cannot register " + uri);
            return null;
        }
        String token = newToken();
        String id = newToken();
        String name = queryName(uri);
        long size = querySize(uri);
        boolean seekable = isSeekable(uri);

        if (!seekable || size <= 0) {
            // Some providers (Google Drive, several OEM cloud backends) expose a
            // streamed or unknown-length descriptor that cannot be read with
            // random access. Spool just those, once; the common path never pays a
            // copy.
            File spool = spoolFile(token);
            long spooled = copyToSpool(uri, spool);
            if (spooled <= 0) {
                return null;
            }
            Entry entry = new Entry(token, id, name, spooled, "application/octet-stream", null, spool);
            entries.put(id, entry);
            return new Picked(spool.getAbsolutePath(), name, spooled, entry.mime);
        }

        Entry entry = new Entry(token, id, name, size, guessMime(name), uri, null);
        entries.put(id, entry);
        return new Picked(sourceFor(entry), name, size, entry.mime);
    }

    /** Drop a registered source once its transfer has finished. */
    public void release(String source) {
        if (source == null) {
            return;
        }
        Entry entry = findEntry(source);
        if (entry == null) {
            return;
        }
        entries.remove(entry.id);
        if (entry.spool != null) {
            //noinspection ResultOfMethodCallIgnored
            entry.spool.delete();
        }
    }

    /** Release without starting a server that was never used. */
    public static void releaseIfRunning(Context context, String source) {
        PickReadServer s;
        synchronized (PickReadServer.class) {
            s = instance;
        }
        if (s != null) {
            s.release(source);
        }
    }

    // ---- request handling --------------------------------------------------

    private void handle(Socket client) {
        try (Socket sock = client) {
            sock.setSoTimeout(30_000);
            InputStream in = new java.io.BufferedInputStream(sock.getInputStream(), 8 << 10);
            OutputStream out = new BufferedOutputStream(sock.getOutputStream(), 64 << 10);

            Request req = Request.read(in);
            if (req == null) {
                return;
            }
            if (!"GET".equals(req.method) && !"HEAD".equals(req.method)) {
                respondError(out, 405, "method not allowed");
                return;
            }
            // Defence in depth alongside the token: the Host header must name
            // this server's own loopback authority.
            if (!hostAllowed(req.host)) {
                respondError(out, 403, "forbidden");
                return;
            }
            String[] seg = splitPath(req.target);
            if (seg.length < 2) {
                respondError(out, 404, "not found");
                return;
            }
            Entry entry = entries.get(seg[1]);
            if (entry == null || !constantTimeEquals(entry.token, seg[0])) {
                respondError(out, 404, "not found");
                return;
            }
            serveEntry(out, req, entry);
        } catch (java.net.SocketTimeoutException ignored) {
            // Idle client; nothing to clean up beyond the try-with-resources.
        } catch (Exception e) {
            Log.w(TAG, "request failed", e);
        }
    }

    private void serveEntry(OutputStream out, Request req, Entry entry) throws IOException {
        long size = entry.size;
        Range range = Range.parse(req.rangeHeader, size);

        if (range.unsatisfiable) {
            respondRangeError(out, size);
            return;
        }
        long start = range.start;
        long end = range.end; // inclusive; -1 means "to the end of file"
        long length = (end < 0 ? size : end + 1) - start;

        boolean partial = req.rangeHeader != null;
        writeStatusLine(out, partial ? 206 : 200);
        out.write(("Content-Length: " + length + "\r\n").getBytes(StandardCharsets.US_ASCII));
        out.write(("Content-Type: " + entry.mime + "\r\n").getBytes(StandardCharsets.US_ASCII));
        out.write("Accept-Ranges: bytes\r\n".getBytes(StandardCharsets.US_ASCII));
        if (partial) {
            out.write(("Content-Range: bytes " + start + "-" + (start + length - 1) + "/" + size + "\r\n")
                    .getBytes(StandardCharsets.US_ASCII));
        }
        out.write("\r\n".getBytes(StandardCharsets.US_ASCII));
        out.flush();

        if ("HEAD".equals(req.method)) {
            return;
        }
        copyRange(out, entry, start, length);
    }

    private void copyRange(OutputStream out, Entry entry, long start, long length) throws IOException {
        if (entry.spool != null) {
            copyFile(out, entry.spool, start, length);
            return;
        }
        ContentResolver resolver = context.getContentResolver();
        android.content.res.AssetFileDescriptor afd = null;
        InputStream raw = null;
        try {
            afd = resolver.openAssetFileDescriptor(entry.uri, "r");
            if (afd == null) {
                return;
            }
            raw = afd.createInputStream();
            // Always seek by skipping rather than with FileChannel.transferTo.
            // AOSP's OffsetCorrectFileChannel passes transferTo's position
            // straight to the delegate, ignoring the descriptor's start offset,
            // while skip() is correctly relative to it — so the sequential path
            // is the only one that reads the right bytes for every provider.
            skipFully(raw, start);
            pump(raw, out, length);
        } finally {
            closeQuietly(raw);
            if (afd != null) {
                try {
                    afd.close();
                } catch (IOException ignored) {
                    // Already closed with the stream.
                }
            }
        }
    }

    private void copyFile(OutputStream out, File file, long start, long length) throws IOException {
        FileInputStream in = new FileInputStream(file);
        try {
            skipFully(in, start);
            pump(in, out, length);
        } finally {
            closeQuietly(in);
        }
    }

    private void skipFully(InputStream in, long count) throws IOException {
        long remaining = count;
        while (remaining > 0) {
            long n = in.skip(remaining);
            if (n > 0) {
                remaining -= n;
                continue;
            }
            if (in.read() < 0) {
                return;
            }
            remaining--;
        }
    }

    private void pump(InputStream in, OutputStream out, long length) throws IOException {
        byte[] buf = new byte[COPY_CHUNK];
        long moved = 0;
        while (moved < length) {
            int want = (int) Math.min(buf.length, length - moved);
            int n = in.read(buf, 0, want);
            if (n < 0) {
                // The provider ended early. Truncate rather than serving a short
                // body silently; the sender detects the mismatch via Content-Length.
                Log.w(TAG, "source ended " + (length - moved) + " bytes early");
                return;
            }
            out.write(buf, 0, n);
            moved += n;
        }
        out.flush();
    }

    // ---- metadata ----------------------------------------------------------

    private String queryName(Uri uri) {
        try (Cursor c = context.getContentResolver().query(
                uri, new String[]{OpenableColumns.DISPLAY_NAME}, null, null, null)) {
            if (c != null && c.moveToFirst()) {
                int idx = c.getColumnIndex(OpenableColumns.DISPLAY_NAME);
                if (idx >= 0 && c.getString(idx) != null) {
                    String n = new File(c.getString(idx)).getName().trim();
                    if (!n.isEmpty()) {
                        return n;
                    }
                }
            }
        } catch (Exception ignored) {
            // Fall through to the placeholder name.
        }
        return "document";
    }

    private long querySize(Uri uri) {
        try (Cursor c = context.getContentResolver().query(
                uri, new String[]{OpenableColumns.SIZE}, null, null, null)) {
            if (c != null && c.moveToFirst()) {
                int idx = c.getColumnIndex(OpenableColumns.SIZE);
                if (idx >= 0 && !c.isNull(idx)) {
                    return c.getLong(idx);
                }
            }
        } catch (Exception ignored) {
            // Unknown; caller falls back to the descriptor or a spool.
        }
        return -1;
    }

    /** True when the provider hands back a descriptor that can be read at an offset. */
    private boolean isSeekable(Uri uri) {
        android.content.res.AssetFileDescriptor afd = null;
        InputStream in = null;
        try {
            afd = context.getContentResolver().openAssetFileDescriptor(uri, "r");
            if (afd == null || afd.getLength() <= 0) {
                return false;
            }
            in = afd.createInputStream();
            if (!(in instanceof FileInputStream)) {
                return false;
            }
            FileChannel ch = ((FileInputStream) in).getChannel();
            if (!ch.isOpen()) {
                return false;
            }
            // A streamed provider reports a size but refuses to seek.
            long pos = ch.position();
            ch.position(pos);
            return true;
        } catch (Exception e) {
            return false;
        } finally {
            closeQuietly(in);
            if (afd != null) {
                try {
                    afd.close();
                } catch (IOException ignored) {
                    // Nothing to do.
                }
            }
        }
    }

    private long copyToSpool(Uri uri, File spool) {
        android.content.res.AssetFileDescriptor afd = null;
        InputStream in = null;
        OutputStream out = null;
        try {
            File parent = spool.getParentFile();
            if (parent != null && !parent.exists() && !parent.mkdirs()) {
                return -1;
            }
            afd = context.getContentResolver().openAssetFileDescriptor(uri, "r");
            if (afd == null) {
                return -1;
            }
            in = afd.createInputStream();
            out = new java.io.FileOutputStream(spool);
            byte[] buf = new byte[COPY_CHUNK];
            long total = 0;
            int n;
            // -1 is the only true end; a 0-byte read must not truncate the spool.
            while ((n = in.read(buf)) != -1) {
                if (n > 0) {
                    out.write(buf, 0, n);
                    total += n;
                }
            }
            out.flush();
            return total;
        } catch (Exception e) {
            Log.w(TAG, "spooling non-seekable source failed", e);
            return -1;
        } finally {
            closeQuietly(in);
            closeQuietly(out);
            if (afd != null) {
                try {
                    afd.close();
                } catch (IOException ignored) {
                    // Nothing to do.
                }
            }
        }
    }

    private File spoolFile(String token) {
        return new File(context.getCacheDir(), "light-spool/" + token);
    }

    private Entry findEntry(String source) {
        for (Entry e : entries.values()) {
            if (source.equals(sourceFor(e)) || (e.spool != null && source.equals(e.spool.getAbsolutePath()))) {
                return e;
            }
        }
        return null;
    }

    private String sourceFor(Entry entry) {
        return "lightpick://127.0.0.1:" + port() + "/" + entry.token + "/" + entry.id + "/" + encode(entry.name);
    }

    private boolean hostAllowed(String host) {
        if (host == null) {
            return false;
        }
        int p = port();
        return host.equals("127.0.0.1:" + p) || host.equals("127.0.0.1") || host.equals("localhost:" + p);
    }

    // ---- helpers -----------------------------------------------------------

    private String newToken() {
        byte[] buf = new byte[32];
        random.nextBytes(buf);
        StringBuilder sb = new StringBuilder(64);
        for (byte b : buf) {
            sb.append(Character.forDigit((b >> 4) & 0xf, 16));
            sb.append(Character.forDigit(b & 0xf, 16));
        }
        return sb.toString();
    }

    private static boolean constantTimeEquals(String a, String b) {
        return MessageDigest.isEqual(a.getBytes(StandardCharsets.UTF_8), b.getBytes(StandardCharsets.UTF_8));
    }

    private static String[] splitPath(String target) {
        int q = target.indexOf('?');
        if (q >= 0) {
            target = target.substring(0, q);
        }
        if (target.startsWith("/")) {
            target = target.substring(1);
        }
        return target.isEmpty() ? new String[0] : target.split("/", -1);
    }

    /** Percent-encode everything outside the URL unreserved set. */
    private static String encode(String s) {
        StringBuilder sb = new StringBuilder(s.length() + 16);
        for (byte b : s.getBytes(StandardCharsets.UTF_8)) {
            int c = b & 0xff;
            boolean unreserved = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
                    || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~';
            if (unreserved) {
                sb.append((char) c);
            } else {
                sb.append('%');
                sb.append(Character.toUpperCase(Character.forDigit((c >> 4) & 0xf, 16)));
                sb.append(Character.toUpperCase(Character.forDigit(c & 0xf, 16)));
            }
        }
        return sb.toString();
    }

    private static String guessMime(String name) {
        String lower = name.toLowerCase(java.util.Locale.US);
        if (lower.endsWith(".mp4")) return "video/mp4";
        if (lower.endsWith(".mkv")) return "video/x-matroska";
        if (lower.endsWith(".mov")) return "video/quicktime";
        if (lower.endsWith(".webm")) return "video/webm";
        if (lower.endsWith(".jpg") || lower.endsWith(".jpeg")) return "image/jpeg";
        if (lower.endsWith(".png")) return "image/png";
        if (lower.endsWith(".webp")) return "image/webp";
        if (lower.endsWith(".gif")) return "image/gif";
        if (lower.endsWith(".heic")) return "image/heic";
        if (lower.endsWith(".mp3")) return "audio/mpeg";
        if (lower.endsWith(".m4a")) return "audio/mp4";
        if (lower.endsWith(".flac")) return "audio/flac";
        if (lower.endsWith(".ogg")) return "audio/ogg";
        if (lower.endsWith(".pdf")) return "application/pdf";
        if (lower.endsWith(".apk")) return "application/vnd.android.package-archive";
        if (lower.endsWith(".zip")) return "application/zip";
        return "application/octet-stream";
    }

    private static void closeQuietly(java.io.Closeable c) {
        if (c != null) {
            try {
                c.close();
            } catch (IOException ignored) {
                // Best effort.
            }
        }
    }

    private static void respondError(OutputStream out, int status, String message) throws IOException {
        writeStatusLine(out, status);
        byte[] body = (message + "\n").getBytes(StandardCharsets.UTF_8);
        out.write(("Content-Length: " + body.length + "\r\n").getBytes(StandardCharsets.US_ASCII));
        out.write("Content-Type: text/plain\r\n\r\n".getBytes(StandardCharsets.US_ASCII));
        out.write(body);
        out.flush();
    }

    /** 416 must still advertise the real length so the client can tell truncation from refusal. */
    private static void respondRangeError(OutputStream out, long size) throws IOException {
        writeStatusLine(out, 416);
        out.write(("Content-Length: 0\r\n").getBytes(StandardCharsets.US_ASCII));
        out.write(("Content-Range: bytes */" + size + "\r\n").getBytes(StandardCharsets.US_ASCII));
        out.write("Accept-Ranges: bytes\r\n\r\n".getBytes(StandardCharsets.US_ASCII));
        out.flush();
    }

    private static void writeStatusLine(OutputStream out, int status) throws IOException {
        String reason;
        switch (status) {
            case 200: reason = "OK"; break;
            case 206: reason = "Partial Content"; break;
            case 403: reason = "Forbidden"; break;
            case 404: reason = "Not Found"; break;
            case 405: reason = "Method Not Allowed"; break;
            case 416: reason = "Range Not Satisfiable"; break;
            default: reason = "Error";
        }
        out.write(("HTTP/1.1 " + status + " " + reason + "\r\n").getBytes(StandardCharsets.US_ASCII));
    }

    // ---- value types -------------------------------------------------------

    /** A picked source handed to the Go uploader. */
    public static final class Picked {
        public final String source;
        public final String name;
        public final long size;
        public final String mime;

        Picked(String source, String name, long size, String mime) {
            this.source = source;
            this.name = name;
            this.size = size;
            this.mime = mime;
        }
    }

    private static final class Entry {
        final String token;
        final String id;
        final String name;
        final long size;
        final String mime;
        /** Non-null when the provider was not seekable and the file was spooled. */
        final File spool;
        /** The picked content:// URI; null for spooled entries. */
        final Uri uri;

        Entry(String token, String id, String name, long size, String mime, Uri uri, File spool) {
            this.token = token;
            this.id = id;
            this.name = name;
            this.size = size;
            this.mime = mime;
            this.uri = uri;
            this.spool = spool;
        }
    }

    private static final class Request {
        String method;
        String target;
        String host;
        String rangeHeader;

        static Request read(InputStream in) throws IOException {
            String line = readLine(in);
            if (line == null || line.isEmpty()) {
                return null;
            }
            String[] parts = line.split(" ");
            if (parts.length < 2) {
                return null;
            }
            Request r = new Request();
            r.method = parts[0];
            r.target = parts[1];
            String header;
            while ((header = readLine(in)) != null && !header.isEmpty()) {
                int colon = header.indexOf(':');
                if (colon <= 0) {
                    continue;
                }
                String key = header.substring(0, colon).trim().toLowerCase(java.util.Locale.US);
                String value = header.substring(colon + 1).trim();
                if ("host".equals(key)) {
                    r.host = value;
                } else if ("range".equals(key)) {
                    r.rangeHeader = value;
                }
            }
            return r;
        }

        private static String readLine(InputStream in) throws IOException {
            ByteBuffer buf = ByteBuffer.allocate(512);
            int c;
            while ((c = in.read()) != -1) {
                if (c == '\n') {
                    byte[] bytes = new byte[buf.position()];
                    buf.flip();
                    buf.get(bytes);
                    String s = new String(bytes, StandardCharsets.ISO_8859_1);
                    return s.endsWith("\r") ? s.substring(0, s.length() - 1) : s;
                }
                if (c != '\r') {
                    if (!buf.hasRemaining()) {
                        // A single header line longer than the buffer: give up on
                        // this request rather than desynchronising the stream.
                        return "";
                    }
                    buf.put((byte) c);
                }
            }
            return null;
        }
    }

    /** A parsed single-range request. {@code end} is inclusive, or -1 for open-ended. */
    private static final class Range {
        long start;
        long end = -1;
        boolean unsatisfiable;

        static Range parse(String header, long size) {
            Range r = new Range();
            if (header == null || !header.startsWith("bytes=") || size <= 0) {
                r.start = 0;
                return r;
            }
            String spec = header.substring(6).trim();
            int dash = spec.indexOf('-');
            if (dash < 0) {
                r.unsatisfiable = true;
                return r;
            }
            try {
                String lo = spec.substring(0, dash).trim();
                String hi = spec.substring(dash + 1).trim();
                if (lo.isEmpty()) {
                    // Suffix form: the last N bytes.
                    long suffix = Long.parseLong(hi);
                    if (suffix <= 0) {
                        r.unsatisfiable = true;
                        return r;
                    }
                    r.start = Math.max(0, size - suffix);
                } else {
                    r.start = Long.parseLong(lo);
                    if (!hi.isEmpty()) {
                        r.end = Math.min(Long.parseLong(hi), size - 1);
                    }
                }
            } catch (NumberFormatException e) {
                r.unsatisfiable = true;
                return r;
            }
            if (r.start < 0 || r.start >= size) {
                r.unsatisfiable = true;
                return r;
            }
            if (r.end >= 0 && r.end < r.start) {
                // e.g. "bytes=5-2": a malformed range, not a zero-length one.
                r.unsatisfiable = true;
            }
            return r;
        }
    }
}