# Teams Channel Notifications — Product Specification

## Purpose

Arbitron currently surfaces notifications only in-app (the dashboard notification
bell). This feature adds an external delivery channel: forwarding notification
events to a Microsoft Teams channel via a Teams workflow webhook, so users can be
alerted without continuously watching the dashboard. The initial version targets a
single global channel and is intended for local testing.

## Users

- **Admins** configure the global Teams webhook (URL plus an on/off toggle) in the
  existing System Configuration area.
- **All users** benefit: their notification events are mirrored to the shared Teams
  channel.

## Features

- A global, admin-only setting: a Teams workflow webhook URL and an enable/disable
  toggle.
- When enabled, **every** in-app notification — needs-interaction, failed,
  completed, and any future notification types — is **also** delivered to the
  configured Teams channel. Teams delivery is in addition to the in-app bell and
  never replaces it.
- Each Teams message includes: a title, the event type, the workflow/instance it
  concerns, a timestamp, and a **deep link** back to that instance in the dashboard.
- Delivery is **best-effort and silent**: failures (Teams unreachable, invalid URL,
  non-success response) are logged and otherwise ignored. They never block workflow
  execution and never affect the in-app notification.
- When the webhook is unset or disabled, behavior is exactly as it is today: in-app
  notifications only, with no Teams delivery and no errors.

## Outcomes / Success Criteria

- With a valid webhook configured and enabled, every notification-raising event
  produces a corresponding Teams message containing the context above and a working
  deep link back to the relevant instance.
- The in-app notification bell continues to function identically whether Teams
  forwarding is on or off.
- A misconfigured or failing webhook causes no workflow disruption and no loss of
  in-app notifications.

## Non-Goals (YAGNI)

- Per-user, per-workflow, or per-event-type routing or filtering.
- Multiple channels or additional transports (email, Slack, etc.).
- Interactive or actionable messages (acting on a notification from inside Teams).
- Retries, delivery guarantees, or delivery-status reporting.
- Privacy isolation within the shared channel: all users' notification events are
  visible to every member of the channel. This is an accepted consequence of a
  single global channel for this version.
