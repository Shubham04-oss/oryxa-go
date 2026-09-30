# Home desk visual QA

Captured on 2026-09-30 from the implemented `WorkspaceShell` and Home desk,
using a temporary local API fixture harness. Desktop viewport: 1440 × 960.
Phone viewport: 423 × 927. The harness was removed before the production build;
these images are review evidence, not a production route or production data.

| State | Desktop | Phone |
| --- | --- | --- |
| Busy | [Composer and decisions](busy-desktop.jpg) | [Composer and decisions](busy-phone.jpg) |
| Busy, continued | [Review and access](busy-desktop-access.jpg) | [Review and access](busy-phone-access.jpg) |
| Busy, continued | [Answers and setup](busy-desktop-shared.jpg) | [Answers and setup](busy-phone-shared.jpg) |
| Quiet, setup complete | [Quiet](quiet-desktop.jpg) | [Quiet](quiet-phone.jpg) |
| Empty, setup incomplete | [Empty](empty-desktop.jpg) | [Empty](empty-phone.jpg) |
| History open | [Home draft retained](history-desktop.jpg) | [Phone overlay](history-phone.jpg) |

The busy desk scrolls inside the canvas; continuation images show lower sections
with the fixed header and rails still in place. All conversations opens history
again after it has been closed, without clearing the Home draft. End access
removes the owner's window after the mock server confirms revocation, while the
asker's window remains. The confirmation row opens its exact chat and keeps
Today as context. The phone canvas has no horizontal overflow.

Chat streaming is not verified by these screenshots: the local development
Durable Object routes are unavailable. Focused tests cover the existing request
queue, draft preservation, history opening and access-refusal behavior.
