import type { LocalizedUpdate } from '@/i18n/content'

// announcements.types.ts describes localized notices, publication settings, and recipient
// targeting. Targeting groups combine their conditions, while user-facing notices include
// reading state and administrator endpoints expose publication metadata and the
// recipients eligible to receive a notice.

// AnnouncementStatus identifies the publication lifecycle state of an administrator
// announcement.
export type AnnouncementStatus = 'draft' | 'active' | 'archived'

// AnnouncementNotifyMode selects silent delivery or popup presentation for an
// announcement.
export type AnnouncementNotifyMode = 'silent' | 'popup'

// AnnouncementConditionType selects subscription membership or balance for audience
// comparison.
export type AnnouncementConditionType = 'subscription' | 'balance'

// AnnouncementOperator identifies the comparison applied to an announcement audience
// condition.
export type AnnouncementOperator = 'in' | 'gt' | 'gte' | 'lt' | 'lte' | 'eq'

// AnnouncementCondition compares a user's subscription or balance against the configured
// values.
export interface AnnouncementCondition {
  type: AnnouncementConditionType // Record kind discriminator.
  operator: AnnouncementOperator // Audience comparison operator.
  plan_ids?: number[] // Matching subscription plan identifiers.
  value?: number // Balance comparison threshold.
}

// AnnouncementConditionGroup combines conditions that must all match an announcement
// recipient.
export interface AnnouncementConditionGroup {
  all_of?: AnnouncementCondition[] // Conditions required together.
}

// AnnouncementTargeting contains alternative condition groups selecting eligible
// announcement recipients.
export interface AnnouncementTargeting {
  any_of?: AnnouncementConditionGroup[] // Alternative eligible condition groups.
}

// Announcement contains administrator publication settings and localized content for one
// notice.
export interface Announcement {
  localization: LocalizedUpdate<{
    title: string // Displayed section title.
    content: string // Published notice content.
  }> // Localized content versions.
  id: number // Record numeric identifier.
  title: string // Displayed section title.
  content: string // Published notice content.
  status: AnnouncementStatus // Current lifecycle state.
  notify_mode: AnnouncementNotifyMode // Announcement delivery mode.
  targeting: AnnouncementTargeting // Recipient eligibility conditions.
  starts_at?: string // Activation start timestamp.
  ends_at?: string // Publication end timestamp.
  created_by?: number // Creating administrator identifier.
  updated_by?: number // Updating administrator identifier.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
}

// UserAnnouncement contains recipient-visible notice content and the user's read
// timestamp.
export interface UserAnnouncement {
  id: number // Record numeric identifier.
  title: string // Displayed section title.
  content: string // Published notice content.
  notify_mode: AnnouncementNotifyMode // Announcement delivery mode.
  starts_at?: string // Activation start timestamp.
  ends_at?: string // Publication end timestamp.
  read_at?: string // Recipient read timestamp.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
}

// CreateAnnouncementRequest supplies localized notice content and its initial publication
// settings.
export interface CreateAnnouncementRequest {
  localization: LocalizedUpdate<{
    title: string // Displayed section title.
    content: string // Published notice content.
  }> // Localized content versions.
  title: string // Displayed section title.
  content: string // Published notice content.
  status?: AnnouncementStatus // Current lifecycle state.
  notify_mode?: AnnouncementNotifyMode // Announcement delivery mode.
  targeting: AnnouncementTargeting // Recipient eligibility conditions.
  starts_at?: number // Activation start in Unix seconds.
  ends_at?: number // Publication end in Unix seconds.
}

// UpdateAnnouncementRequest supplies optional changes to notice content and publication
// settings.
export interface UpdateAnnouncementRequest {
  localization?: LocalizedUpdate<{
    title: string // Displayed section title.
    content: string // Published notice content.
  }> // Localized content versions.
  title?: string // Displayed section title.
  content?: string // Published notice content.
  status?: AnnouncementStatus // Current lifecycle state.
  notify_mode?: AnnouncementNotifyMode // Announcement delivery mode.
  targeting?: AnnouncementTargeting // Recipient eligibility conditions.
  starts_at?: number // Activation start in Unix seconds.
  ends_at?: number // Publication end in Unix seconds.
}

// AnnouncementUserReadStatus describes whether one eligible account has read an
// announcement.
export interface AnnouncementUserReadStatus {
  user_id: number // Owning account identifier.
  email: string // Account email address.
  username: string // Account display username.
  balance: number // Available wallet balance.
  eligible: boolean // Recipient eligibility result.
  read_at?: string // Recipient read timestamp.
}
