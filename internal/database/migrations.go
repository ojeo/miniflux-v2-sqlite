// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package database // import "miniflux.app/v2/internal/database"

import (
	"database/sql"
	"fmt"
)

// schemaVersion is the number of registered migrations. SQLite migrations are
// consolidated into a single initial schema (version 1), so this stays at 1.
var schemaVersion = len(migrations)

// migrations is the ordered list of schema migrations. For SQLite the whole
// Miniflux schema is created once by migration 0; the versioning mechanism is
// kept so future incremental migrations can be appended here.
var migrations = [...]func(tx *sql.Tx) error{
	func(tx *sql.Tx) (err error) {
		statements := []string{
			// ---------------------------------------------------------------------
			// Versioning
			// ---------------------------------------------------------------------
			`CREATE TABLE IF NOT EXISTS schema_version (version integer not null)`,

			// ---------------------------------------------------------------------
			// Users
			// ---------------------------------------------------------------------
			`CREATE TABLE users (
				id integer primary key autoincrement,
				username text not null unique,
				password text,
				is_admin integer not null default 0,
				language text not null default 'en_US',
				timezone text not null default 'UTC',
				theme text not null default 'light_serif',
				last_login_at text,
				entry_direction text not null default 'asc',
				keyboard_shortcuts integer not null default 1,
				entry_swipe integer not null default 1,
				entries_per_page integer not null default 100,
				show_reading_time integer not null default 1,
				stylesheet text not null default '',
				google_id text not null default '',
				openid_connect_id text not null default '',
				display_mode text not null default 'standalone',
				entry_order text not null default 'published_at',
				default_reading_speed integer not null default 265,
				cjk_reading_speed integer not null default 500,
				default_home_page text not null default 'unread',
				categories_sorting_order text not null default 'unread_count',
				mark_read_on_view integer not null default 1,
				gesture_nav text not null default 'tap',
				media_playback_rate real not null default 1,
				mark_read_on_media_player_completion integer not null default 0,
				custom_js text not null default '',
				external_font_hosts text not null default '',
				always_open_external_links integer not null default 0,
				open_external_links_in_new_tab integer not null default 1,
				block_filter_entry_rules text not null default '',
				keep_filter_entry_rules text not null default ''
			)`,

			`CREATE UNIQUE INDEX users_google_id_idx ON users(google_id) WHERE google_id <> ''`,
			`CREATE UNIQUE INDEX users_openid_connect_id_idx ON users(openid_connect_id) WHERE openid_connect_id <> ''`,

			// ---------------------------------------------------------------------
			// Categories
			// ---------------------------------------------------------------------
			`CREATE TABLE categories (
				id integer primary key autoincrement,
				user_id integer not null,
				title text not null,
				hide_globally integer not null default 0,
				unique (user_id, title),
				foreign key (user_id) references users(id) on delete cascade
			)`,

			// ---------------------------------------------------------------------
			// Feeds
			// ---------------------------------------------------------------------
			`CREATE TABLE feeds (
				id integer primary key autoincrement,
				user_id integer not null,
				category_id integer not null,
				title text not null,
				feed_url text not null,
				site_url text not null,
				checked_at text not null default (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
				etag_header text not null default '',
				last_modified_header text not null default '',
				parsing_error_msg text not null default '',
				parsing_error_count integer not null default 0,
				scraper_rules text not null default '',
				rewrite_rules text not null default '',
				crawler integer not null default 0,
				username text not null default '',
				password text not null default '',
				user_agent text not null default '',
				disabled integer not null default 0,
				next_check_at text,
				ignore_http_cache integer not null default 0,
				fetch_via_proxy integer not null default 0,
				blocklist_rules text not null default '',
				block_filter_entry_rules text not null default '',
				keep_filter_entry_rules text not null default '',
				keeplist_rules text not null default '',
				allow_self_signed_certificates integer not null default 0,
				cookie text not null default '',
				hide_globally integer not null default 0,
				url_rewrite_rules text not null default '',
				no_media_player integer not null default 0,
				description text not null default '',
				ntfy_enabled integer not null default 0,
				ntfy_priority integer not null default 3,
				ntfy_topic text not null default '',
				pushover_enabled integer not null default 0,
				pushover_priority integer not null default 0,
				proxy_url text not null default '',
				webhook_url text not null default '',
				disable_http2 integer not null default 0,
				ignore_entry_updates integer not null default 0,
				apprise_service_urls text not null default '',
				language text not null default '',
				unique (user_id, feed_url),
				foreign key (user_id) references users(id) on delete cascade,
				foreign key (category_id) references categories(id) on delete cascade
			)`,

			`CREATE INDEX feeds_user_category_idx ON feeds(user_id, category_id)`,
			`CREATE INDEX feeds_feed_id_hide_globally_idx ON feeds(id, hide_globally)`,

			// ---------------------------------------------------------------------
			// Entries
			// ---------------------------------------------------------------------
			`CREATE TABLE entries (
				id integer primary key autoincrement,
				user_id integer not null,
				feed_id integer not null,
				hash text not null,
				published_at text not null,
				title text not null,
				url text not null,
				author text,
				content text,
				status text not null default 'unread',
				starred integer not null default 0,
				comments_url text not null default '',
				changed_at text not null,
				created_at text not null default (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
				share_code text not null default '',
				tags text not null default '[]',
				reading_time integer not null default 0,
				language text not null default '',
				unique (feed_id, hash),
				foreign key (user_id) references users(id) on delete cascade,
				foreign key (feed_id) references feeds(id) on delete cascade
			)`,

			`CREATE INDEX entries_feed_idx ON entries(feed_id)`,
			`CREATE INDEX entries_user_status_idx ON entries(user_id, status)`,
			`CREATE INDEX entries_user_feed_idx ON entries(user_id, feed_id)`,
			`CREATE INDEX entries_feed_id_status_hash_idx ON entries(feed_id, status, hash)`,
			`CREATE INDEX entries_id_user_status_idx ON entries(id, user_id, status)`,
			`CREATE INDEX entries_user_id_status_starred_idx ON entries(user_id, status, starred)`,
			`CREATE INDEX entries_user_status_feed_idx ON entries(user_id, status, feed_id)`,
			`CREATE INDEX entries_user_status_changed_idx ON entries(user_id, status, changed_at)`,
			`CREATE INDEX entries_user_status_published_idx ON entries(user_id, status, published_at)`,
			`CREATE INDEX entries_user_status_created_idx ON entries(user_id, status, created_at)`,
			`CREATE INDEX entries_user_status_changed_published_idx ON entries(user_id, status, changed_at, published_at)`,
			`CREATE UNIQUE INDEX entries_share_code_idx ON entries(share_code) WHERE share_code <> ''`,

			// ---------------------------------------------------------------------
			// Enclosures
			// ---------------------------------------------------------------------
			`CREATE TABLE enclosures (
				id integer primary key autoincrement,
				user_id integer not null,
				entry_id integer not null,
				url text not null,
				size integer not null default 0,
				mime_type text not null default '',
				media_progression integer not null default 0,
				foreign key (user_id) references users(id) on delete cascade,
				foreign key (entry_id) references entries(id) on delete cascade
			)`,

			`CREATE UNIQUE INDEX enclosures_user_entry_url_unique_idx ON enclosures(user_id, entry_id, url)`,
			`CREATE INDEX enclosures_entry_id_idx ON enclosures(entry_id)`,

			// ---------------------------------------------------------------------
			// Icons
			// ---------------------------------------------------------------------
			`CREATE TABLE icons (
				id integer primary key autoincrement,
				hash text not null unique,
				mime_type text not null,
				content blob not null,
				external_id text not null default ''
			)`,

			`CREATE UNIQUE INDEX icons_external_id_idx ON icons(external_id) WHERE external_id <> ''`,

			`CREATE TABLE feed_icons (
				feed_id integer not null,
				icon_id integer not null,
				primary key(feed_id, icon_id),
				foreign key (feed_id) references feeds(id) on delete cascade,
				foreign key (icon_id) references icons(id) on delete cascade
			)`,

			// ---------------------------------------------------------------------
			// Integrations
			// ---------------------------------------------------------------------
			`CREATE TABLE integrations (
				user_id integer not null,
				pinboard_enabled integer not null default 0,
				pinboard_token text not null default '',
				pinboard_tags text not null default 'miniflux',
				pinboard_mark_as_unread integer not null default 0,
				instapaper_enabled integer not null default 0,
				instapaper_username text not null default '',
				instapaper_password text not null default '',
				fever_enabled integer not null default 0,
				fever_username text not null default '',
				fever_token text not null default '',
				wallabag_enabled integer not null default 0,
				wallabag_url text not null default '',
				wallabag_client_id text not null default '',
				wallabag_client_secret text not null default '',
				wallabag_username text not null default '',
				wallabag_password text not null default '',
				wallabag_only_url integer not null default 0,
				wallabag_tags text not null default '',
				nunux_keeper_enabled integer not null default 0,
				nunux_keeper_url text not null default '',
				nunux_keeper_api_key text not null default '',
				telegram_bot_enabled integer not null default 0,
				telegram_bot_token text not null default '',
				telegram_bot_chat_id text not null default '',
				telegram_bot_topic_id integer,
				telegram_bot_disable_web_page_preview integer not null default 0,
				telegram_bot_disable_notification integer not null default 0,
				telegram_bot_disable_buttons integer not null default 0,
				googlereader_enabled integer not null default 0,
				googlereader_username text not null default '',
				googlereader_password text not null default '',
				espial_enabled integer not null default 0,
				espial_url text not null default '',
				espial_api_key text not null default '',
				espial_tags text not null default 'miniflux',
				linkding_enabled integer not null default 0,
				linkding_url text not null default '',
				linkding_api_key text not null default '',
				linkding_tags text not null default '',
				linkding_mark_as_unread integer not null default 0,
				matrix_bot_enabled integer not null default 0,
				matrix_bot_user text not null default '',
				matrix_bot_password text not null default '',
				matrix_bot_url text not null default '',
				matrix_bot_chat_id text not null default '',
				notion_enabled integer not null default 0,
				notion_token text not null default '',
				notion_page_id text not null default '',
				readwise_enabled integer not null default 0,
				readwise_api_key text not null default '',
				apprise_enabled integer not null default 0,
				apprise_url text not null default '',
				apprise_services_url text not null default '',
				shiori_enabled integer not null default 0,
				shiori_url text not null default '',
				shiori_username text not null default '',
				shiori_password text not null default '',
				shaarli_enabled integer not null default 0,
				shaarli_url text not null default '',
				shaarli_api_secret text not null default '',
				rssbridge_enabled integer not null default 0,
				rssbridge_url text not null default '',
				rssbridge_token text not null default '',
				webhook_enabled integer not null default 0,
				webhook_url text not null default '',
				webhook_secret text not null default '',
				omnivore_enabled integer not null default 0,
				omnivore_api_key text not null default '',
				omnivore_url text not null default '',
				linkace_enabled integer not null default 0,
				linkace_url text not null default '',
				linkace_api_key text not null default '',
				linkace_tags text not null default '',
				linkace_is_private integer not null default 1,
				linkace_check_disabled integer not null default 1,
				linkwarden_enabled integer not null default 0,
				linkwarden_url text not null default '',
				linkwarden_api_key text not null default '',
				linkwarden_collection_id integer,
				readeck_enabled integer not null default 0,
				readeck_only_url integer not null default 0,
				readeck_url text not null default '',
				readeck_api_key text not null default '',
				readeck_labels text not null default '',
				readeck_push_enabled integer not null default 0,
				raindrop_enabled integer not null default 0,
				raindrop_token text not null default '',
				raindrop_collection_id text not null default '',
				raindrop_tags text not null default '',
				betula_url text not null default '',
				betula_token text not null default '',
				betula_enabled integer not null default 0,
				ntfy_enabled integer not null default 0,
				ntfy_url text not null default '',
				ntfy_topic text not null default '',
				ntfy_api_token text not null default '',
				ntfy_username text not null default '',
				ntfy_password text not null default '',
				ntfy_icon_url text not null default '',
				ntfy_internal_links integer not null default 0,
				slack_enabled integer not null default 0,
				slack_webhook_link text not null default '',
				karakeep_enabled integer not null default 0,
				karakeep_api_key text not null default '',
				karakeep_url text not null default '',
				karakeep_tags text not null default '',
				cubox_enabled integer not null default 0,
				cubox_api_link text not null default '',
				discord_enabled integer not null default 0,
				discord_webhook_link text not null default '',
				archiveorg_enabled integer not null default 0,
				pushover_enabled integer not null default 0,
				pushover_user text not null default '',
				pushover_token text not null default '',
				pushover_device text not null default '',
				pushover_prefix text not null default '',
				linktaco_enabled integer not null default 0,
				linktaco_api_token text not null default '',
				linktaco_org_slug text not null default '',
				linktaco_tags text not null default '',
				linktaco_visibility text not null default 'PUBLIC',
				primary key(user_id),
				foreign key (user_id) references users(id) on delete cascade
			)`,

			// ---------------------------------------------------------------------
			// API keys
			// ---------------------------------------------------------------------
			`CREATE TABLE api_keys (
				id integer primary key autoincrement,
				user_id integer not null,
				token text not null unique,
				description text not null,
				last_used_at text,
				created_at text not null default (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
				unique (user_id, description),
				foreign key (user_id) references users(id) on delete cascade
			)`,

			// ---------------------------------------------------------------------
			// ACME cache
			// ---------------------------------------------------------------------
			`CREATE TABLE acme_cache (
				key text not null primary key,
				data blob not null,
				updated_at text not null
			)`,

			// ---------------------------------------------------------------------
			// WebAuthn credentials
			// ---------------------------------------------------------------------
			`CREATE TABLE webauthn_credentials (
				handle blob primary key,
				cred_id blob unique not null,
				user_id integer not null,
				public_key blob not null,
				attestation_type text not null,
				aaguid blob,
				sign_count integer,
				clone_warning integer,
				name text not null default '',
				added_on text not null default (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
				last_seen_on text not null default (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
				backup_eligible integer,
				backup_state integer not null default 0,
				foreign key (user_id) references users(id) on delete cascade
			)`,

			// ---------------------------------------------------------------------
			// Web sessions
			// ---------------------------------------------------------------------
			`CREATE TABLE web_sessions (
				id text not null primary key,
				secret_hash blob not null,
				user_id integer,
				created_at text not null default (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
				user_agent text not null default '',
				ip text,
				state text not null default '{}',
				foreign key (user_id) references users(id) on delete cascade
			)`,

			`CREATE INDEX web_sessions_user_id_idx ON web_sessions(user_id) WHERE user_id IS NOT NULL`,
			`CREATE INDEX web_sessions_created_at_idx ON web_sessions(created_at)`,

			// ---------------------------------------------------------------------
			// Entry tombstones
			// ---------------------------------------------------------------------
			`CREATE TABLE entry_tombstones (
				feed_id integer not null,
				hash text not null,
				deleted_at text not null default (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
				primary key (feed_id, hash),
				foreign key (feed_id) references feeds(id) on delete cascade
			)`,

			`CREATE INDEX entry_tombstones_deleted_at_idx ON entry_tombstones(deleted_at)`,

			// ---------------------------------------------------------------------
			// Full-text search (FTS5) - degrades PostgreSQL tsvector search.
			// ---------------------------------------------------------------------
			`CREATE VIRTUAL TABLE entries_fts USING fts5(
				title, content, author, tags,
				content='entries', content_rowid='id',
				tokenize='unicode61'
			)`,

			`CREATE TRIGGER entries_ai AFTER INSERT ON entries BEGIN
				INSERT INTO entries_fts(rowid, title, content, author, tags)
				VALUES (new.id, new.title, new.content, new.author, new.tags);
			END`,

			`CREATE TRIGGER entries_ad AFTER DELETE ON entries BEGIN
				INSERT INTO entries_fts(entries_fts, rowid, title, content, author, tags)
				VALUES ('delete', old.id, old.title, old.content, old.author, old.tags);
			END`,

			`CREATE TRIGGER entries_au AFTER UPDATE ON entries BEGIN
				INSERT INTO entries_fts(entries_fts, rowid, title, content, author, tags)
				VALUES ('delete', old.id, old.title, old.content, old.author, old.tags);
				INSERT INTO entries_fts(rowid, title, content, author, tags)
				VALUES (new.id, new.title, new.content, new.author, new.tags);
			END`,
		}

		for _, stmt := range statements {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("sqlite migration: %v\nSQL: %s", err, stmt)
			}
		}
		return nil
	},
}
