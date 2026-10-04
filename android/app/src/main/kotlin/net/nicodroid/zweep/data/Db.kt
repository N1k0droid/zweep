// Copyright 2026 N1k0droid
// SPDX-License-Identifier: AGPL-3.0-only

package net.nicodroid.zweep.data

import android.content.Context
import androidx.room.ColumnInfo
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Entity
import androidx.room.Index
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.PrimaryKey
import androidx.room.Query
import androidx.room.Room
import androidx.room.RoomDatabase
import androidx.room.Transaction
import androidx.room.Upsert
import androidx.room.migration.Migration
import androidx.sqlite.db.SupportSQLiteDatabase
import kotlinx.coroutines.flow.Flow

/** A Zweep server this device is enrolled on (up to 5) */
@Entity(tableName = "server")
data class ServerEntity(
    @PrimaryKey(autoGenerate = true) val id: Long = 0,
    val label: String,
    val baseUrl: String,
    val urls: String, // service URLs of the same server, newline-separated (failover order)
    val serverId: String,
    val nodeId: String = "",
    val username: String,
    val deviceId: String,
    val tokenEnc: ByteArray,
    val pins: String = "", // SPKI SHA-256 pins, base64, comma-separated (current and backup)
    val cleartextAccepted: Boolean = false, // the user confirmed an unencrypted lab connection
    val ackedSeq: Long = -1,
    val projRev: Long = 0,
    val features: String = "",
    val enabled: Boolean = true,
    val createdAt: Long,
)

/** A received alarm, persisted before it is notified and acknowledged */
@Entity(
    tableName = "message",
    primaryKeys = ["serverRef", "seq"],
    indices = [Index("serverRef", "sid"), Index("dedupKey"), Index("receivedAt"), Index("state")],
)
data class MessageEntity(
    val serverRef: Long,
    val seq: Long,
    val msgId: String,
    val sid: String,
    val ver: Long,
    val kind: String,
    val sev: Int,
    val channels: String,
    val source: String,
    val sourceName: String,
    val eventId: String,
    val host: String,
    val name: String,
    val title: String,
    val body: String,
    val eventTime: Long,
    val receivedAt: Long,
    val dedupKey: String,
    val duplicate: Boolean = false, // the same alarm already came from another server
    val state: String = STATE_STORED,
    val reason: String? = null,
    val notifId: Int = 0,
    val reminders: Int = 0,
    val nextReminderAt: Long = 0,
    val readAt: Long = 0,
    val silenced: Boolean = false,
) {
    companion object {
        const val STATE_STORED = "stored" // persisted, presentation pending
        const val STATE_NOTIFIED = "notified"
        const val STATE_SILENT = "silent"
    }
}

/** A receipt waiting for the connection (persist, notify, then confirm) */
@Entity(tableName = "receipt", indices = [Index("serverRef")])
data class ReceiptEntity(
    @PrimaryKey(autoGenerate = true) val id: Long = 0,
    val serverRef: Long,
    val msgId: String,
    val seq: Long,
    val state: String,
    val reason: String? = null,
)

@Entity(tableName = "problem", primaryKeys = ["serverRef", "source", "eventid"])
data class ProblemEntity(
    val serverRef: Long,
    val source: String,
    val eventid: String,
    val json: String,
    val severity: Int,
    val clock: String,
    val status: String,
)

@Entity(tableName = "source", primaryKeys = ["serverRef", "id"])
data class SourceEntity(
    val serverRef: Long,
    val id: String,
    val name: String,
    val frontendUrl: String = "",
    val apiMode: String = "disabled",
    val stale: Boolean = false,
    val dataAsOf: String = "",
)

/** A Zweep channel of a server with the user's preferences */
@Entity(tableName = "channel", primaryKeys = ["serverRef", "id"])
data class ChannelEntity(
    val serverRef: Long,
    val id: String,
    val kind: String,
    val name: String,
    val serverEnabled: Boolean = true,
    val notify: Boolean = true,
    val reminders: Int = 0,
    val reminderMinutes: Int = 2,
    /** Color chosen by the admin for a custom channel (#RRGGBB from the dashboard palette), empty: none */
    @ColumnInfo(defaultValue = "") val color: String = "",
)

/** An ack typed in the app: queued until the server accepts it, then confirmed or rejected */
@Entity(tableName = "ack", indices = [Index("serverRef", "source", "eventid")])
data class AckEntity(
    @PrimaryKey val requestId: String,
    val serverRef: Long,
    val source: String,
    val eventid: String,
    val text: String,
    val state: String,
    val reason: String? = null,
    val createdAt: Long,
    val updatedAt: Long,
) {
    companion object {
        const val QUEUED = "queued"
        const val ACCEPTED = "accepted"
        const val CONFIRMED = "confirmed"
        const val REJECTED = "rejected"
    }
}

@Entity(tableName = "setting")
data class SettingEntity(@PrimaryKey val key: String, val value: String)

@Dao
interface ServerDao {
    @Query("SELECT * FROM server ORDER BY id")
    fun observe(): Flow<List<ServerEntity>>

    @Query("SELECT * FROM server ORDER BY id")
    suspend fun all(): List<ServerEntity>

    @Query("SELECT * FROM server WHERE id = :id")
    suspend fun get(id: Long): ServerEntity?

    @Insert
    suspend fun insert(s: ServerEntity): Long

    @Query("UPDATE server SET ackedSeq = :seq WHERE id = :id AND ackedSeq < :seq")
    suspend fun advance(id: Long, seq: Long)

    @Query("UPDATE server SET projRev = :rev WHERE id = :id")
    suspend fun setProjRev(id: Long, rev: Long)

    @Query("UPDATE server SET features = :features, nodeId = :nodeId WHERE id = :id")
    suspend fun setWelcome(id: Long, features: String, nodeId: String)

    @Query("UPDATE server SET tokenEnc = :token WHERE id = :id")
    suspend fun setToken(id: Long, token: ByteArray)

    @Query("UPDATE server SET enabled = :enabled WHERE id = :id")
    suspend fun setEnabled(id: Long, enabled: Boolean)

    @Query("UPDATE server SET urls = :urls WHERE id = :id")
    suspend fun setUrls(id: Long, urls: String)

    @Query("DELETE FROM server WHERE id = :id")
    suspend fun delete(id: Long)
}

@Dao
interface MessageDao {
    @Insert(onConflict = OnConflictStrategy.IGNORE)
    suspend fun insert(m: MessageEntity): Long

    @Query("SELECT * FROM message WHERE serverRef = :server AND seq = :seq")
    suspend fun get(server: Long, seq: Long): MessageEntity?

    @Query("SELECT count(*) > 0 FROM message WHERE dedupKey = :key AND serverRef != :server")
    suspend fun seenElsewhere(key: String, server: Long): Boolean

    @Query("SELECT * FROM message WHERE duplicate = 0 ORDER BY receivedAt DESC, seq DESC LIMIT :limit")
    fun observeRecent(limit: Int): Flow<List<MessageEntity>>

    @Query("SELECT * FROM message WHERE serverRef = :server AND sid = :sid ORDER BY ver")
    suspend fun history(server: Long, sid: String): List<MessageEntity>

    @Query("SELECT * FROM message WHERE serverRef = :server AND sid = :sid ORDER BY ver DESC LIMIT 1")
    suspend fun latest(server: Long, sid: String): MessageEntity?

    @Query("SELECT * FROM message WHERE state = 'stored' ORDER BY receivedAt, seq")
    suspend fun pendingPresentation(): List<MessageEntity>

    @Query("SELECT * FROM message WHERE state = 'notified' AND silenced = 0 AND readAt = 0 AND nextReminderAt > 0 AND nextReminderAt <= :now")
    suspend fun dueReminders(now: Long): List<MessageEntity>

    @Query("SELECT min(nextReminderAt) FROM message WHERE state = 'notified' AND silenced = 0 AND readAt = 0 AND nextReminderAt > 0")
    suspend fun nextReminder(): Long?

    @Query("UPDATE message SET state = :state, reason = :reason, notifId = :notifId, nextReminderAt = :next WHERE serverRef = :server AND seq = :seq")
    suspend fun setPresented(server: Long, seq: Long, state: String, reason: String?, notifId: Int, next: Long)

    @Query("UPDATE message SET reminders = reminders + 1, nextReminderAt = :next WHERE serverRef = :server AND seq = :seq")
    suspend fun reminded(server: Long, seq: Long, next: Long)

    @Query("UPDATE message SET nextReminderAt = 0 WHERE serverRef = :server AND sid = :sid")
    suspend fun stopReminders(server: Long, sid: String)

    @Query("UPDATE message SET silenced = 1, nextReminderAt = 0 WHERE serverRef = :server AND sid = :sid")
    suspend fun silence(server: Long, sid: String)

    @Query("UPDATE message SET readAt = :now, nextReminderAt = 0 WHERE serverRef = :server AND sid = :sid AND readAt = 0")
    suspend fun markRead(server: Long, sid: String, now: Long)

    @Query("UPDATE message SET readAt = :now, nextReminderAt = 0 WHERE readAt = 0")
    suspend fun markAllRead(now: Long)

    @Query("SELECT count(*) FROM message WHERE state = 'silent' AND reason IN ('app_dnd', 'old') AND receivedAt >= :since")
    suspend fun silentSince(since: Long): Int

    /**
     * Local history: deletes the alarms whose last message is older than [before], but only closed ones
     * (a recovery arrived) or those without a problem (tests): an alarm still open is never deleted,
     * however short the Local history.
     */
    @Query("""DELETE FROM message WHERE (serverRef, sid) IN (
        SELECT serverRef, sid FROM message GROUP BY serverRef, sid
        HAVING max(receivedAt) < :before AND (sum(kind = 'recovery') > 0 OR sum(kind = 'problem') = 0))""")
    suspend fun purge(before: Long): Int

    @Query("DELETE FROM message WHERE serverRef = :server")
    suspend fun deleteServer(server: Long)
}

@Dao
interface ReceiptDao {
    @Insert
    suspend fun insert(r: ReceiptEntity)

    @Query("SELECT * FROM receipt WHERE serverRef = :server ORDER BY id LIMIT 200")
    suspend fun pending(server: Long): List<ReceiptEntity>

    @Query("DELETE FROM receipt WHERE id IN (:ids)")
    suspend fun delete(ids: List<Long>)

    @Query("DELETE FROM receipt WHERE serverRef = :server")
    suspend fun deleteServer(server: Long)
}

@Dao
interface ProblemDao {
    @Query("SELECT * FROM problem ORDER BY severity DESC, clock DESC")
    fun observe(): Flow<List<ProblemEntity>>

    @Query("SELECT * FROM problem WHERE serverRef = :server")
    suspend fun of(server: Long): List<ProblemEntity>

    @Upsert
    suspend fun upsert(rows: List<ProblemEntity>)

    @Query("DELETE FROM problem WHERE serverRef = :server AND source = :source AND eventid = :eventid")
    suspend fun delete(server: Long, source: String, eventid: String)

    @Query("DELETE FROM problem WHERE serverRef = :server")
    suspend fun deleteServer(server: Long)

    @Transaction
    suspend fun replace(server: Long, rows: List<ProblemEntity>) {
        deleteServer(server)
        upsert(rows)
    }
}

@Dao
interface SourceDao {
    @Query("SELECT * FROM source")
    fun observe(): Flow<List<SourceEntity>>

    @Query("SELECT * FROM source WHERE serverRef = :server AND id = :id")
    suspend fun get(server: Long, id: String): SourceEntity?

    @Query("SELECT * FROM source WHERE serverRef = :server")
    suspend fun of(server: Long): List<SourceEntity>

    @Query("DELETE FROM source WHERE serverRef = :server")
    suspend fun deleteServer(server: Long)

    @Upsert
    suspend fun upsert(rows: List<SourceEntity>)

    @Query("UPDATE source SET stale = :stale WHERE serverRef = :server AND id = :id")
    suspend fun setStale(server: Long, id: String, stale: Boolean)

    @Transaction
    suspend fun replace(server: Long, rows: List<SourceEntity>) {
        deleteServer(server)
        upsert(rows)
    }
}

@Dao
interface ChannelDao {
    @Query("SELECT * FROM channel ORDER BY serverRef, kind DESC, id DESC")
    fun observe(): Flow<List<ChannelEntity>>

    @Query("SELECT * FROM channel WHERE serverRef = :server")
    suspend fun of(server: Long): List<ChannelEntity>

    @Query("SELECT * FROM channel WHERE serverRef = :server AND id = :id")
    suspend fun get(server: Long, id: String): ChannelEntity?

    @Upsert
    suspend fun upsert(rows: List<ChannelEntity>)

    @Query("DELETE FROM channel WHERE serverRef = :server AND id NOT IN (:keep)")
    suspend fun prune(server: Long, keep: List<String>)

    @Query("DELETE FROM channel WHERE serverRef = :server")
    suspend fun deleteServer(server: Long)
}

@Dao
interface AckDao {
    @Insert
    suspend fun insert(a: AckEntity)

    @Query("SELECT * FROM ack WHERE state = 'queued' AND serverRef = :server ORDER BY createdAt")
    suspend fun queued(server: Long): List<AckEntity>

    @Query("SELECT * FROM ack WHERE state = 'accepted' AND serverRef = :server ORDER BY createdAt")
    suspend fun accepted(server: Long): List<AckEntity>

    /**
     * States only move forward (queued → accepted → confirmed/rejected): the ack.result frame can
     * arrive before the response of the POST that created the request
     */
    @Query(
        """UPDATE ack SET state = :state, reason = :reason, updatedAt = :now WHERE requestId = :id AND (
            state = 'queued' OR (state = 'accepted' AND :state IN ('confirmed', 'rejected')))""",
    )
    suspend fun setState(id: String, state: String, reason: String?, now: Long)

    @Query("SELECT * FROM ack WHERE serverRef = :server AND source = :source AND eventid = :eventid ORDER BY createdAt DESC")
    fun observeFor(server: Long, source: String, eventid: String): Flow<List<AckEntity>>

    @Query("DELETE FROM ack WHERE serverRef = :server")
    suspend fun deleteServer(server: Long)
}

@Dao
interface SettingDao {
    @Query("SELECT value FROM setting WHERE `key` = :key")
    suspend fun get(key: String): String?

    @Query("SELECT * FROM setting")
    fun observe(): Flow<List<SettingEntity>>

    @Upsert
    suspend fun put(s: SettingEntity)

    @Query("DELETE FROM setting WHERE `key` = :key")
    suspend fun delete(key: String)

    @Query("DELETE FROM setting WHERE `key` LIKE :prefix || '%'")
    suspend fun deletePrefix(prefix: String)
}

@Database(
    entities = [
        ServerEntity::class, MessageEntity::class, ReceiptEntity::class, ProblemEntity::class,
        SourceEntity::class, ChannelEntity::class, AckEntity::class, SettingEntity::class,
    ],
    version = 2,
)
abstract class ZweepDb : RoomDatabase() {
    abstract fun servers(): ServerDao
    abstract fun messages(): MessageDao
    abstract fun receipts(): ReceiptDao
    abstract fun problems(): ProblemDao
    abstract fun sources(): SourceDao
    abstract fun channels(): ChannelDao
    abstract fun acks(): AckDao
    abstract fun settings(): SettingDao

    /** Removes everything a server brought to this device (logout) */
    @Transaction
    open suspend fun forgetServer(id: Long) {
        messages().deleteServer(id)
        receipts().deleteServer(id)
        problems().deleteServer(id)
        settings().delete(Settings.LIVE_UNTIL + id)
        sources().deleteServer(id)
        channels().deleteServer(id)
        acks().deleteServer(id)
        servers().delete(id)
    }

    companion object {
        @Volatile private var instance: ZweepDb? = null

        /** Channel colors (dashboard palette) */
        val MIGRATION_1_2 = object : Migration(1, 2) {
            override fun migrate(db: SupportSQLiteDatabase) {
                db.execSQL("ALTER TABLE channel ADD COLUMN color TEXT NOT NULL DEFAULT ''")
            }
        }

        /**
         * The database lives in device-protected storage (Direct Boot): alarms can be received
         * after a reboot before the first unlock. It holds no Zabbix secret.
         */
        fun get(context: Context): ZweepDb = instance ?: synchronized(this) {
            instance ?: Room.databaseBuilder(
                context.applicationContext.createDeviceProtectedStorageContext(), ZweepDb::class.java, "zweep.db",
            ).addMigrations(MIGRATION_1_2).build().also { instance = it }
        }
    }
}
