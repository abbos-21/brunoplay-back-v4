# Interstitial ads and the sponsored task (Adsgram)

Two more Adsgram formats besides the rewarded ads of the game. Both are OFF until you switch them on in the admin panel (Settings, group `ads`).

## Interstitial (full screen, the player can close it, nothing is paid)

1. In the Adsgram dashboard create a block of type **Interstitial** and copy its id (digits).
2. Admin, Settings, `ads`:
   * `ads.interstitial.block_id`: that id
   * `ads.interstitial.interval_seconds`: the least time between two ads for one player (default 180)
   * `ads.interstitial.first_after_seconds`: no ad in the first seconds after the game is opened (default 120)
   * `ads.interstitial.enabled`: ON
3. The game shows one when the player **leaves a game** (Ride, Wheel, Chest), if the gap since the player's last ad of any kind (rewarded ads count too) is over. Nothing is shown during a money action, and a failed or missing ad is ignored.

The change applies within about 5 minutes (the game caches the settings), or at once when the game is opened again.

## Sponsored task (Adsgram Task block: "join this channel", "open this app")

1. In the Adsgram dashboard create **one** block of type **Task** (Adsgram allows only one). Its id looks like `task-123`.
2. Set its **Reward URL** to (same secret as the rewarded ads, `ADSGRAM_WEBHOOK_SECRET` in `.env`; keep `[userId]` exactly):

   `https://YOUR-GAME-ADDRESS/api/webhooks/adsgram-task?userid=[userId]&secret=THE_SECRET`

   (do not add `blockid`; it is a different address from the one of the rewarded ads, `.../api/webhooks/adsgram`)
3. Admin, Settings, `ads`:
   * `ads.task.block_id`: `task-123`
   * `ads.task.reward_coins`: coins for one completed task (default 2000)
   * `ads.task.max_per_day`: how many completions pay per player per day (default 3)
   * `ads.task.enabled`: ON
4. The card appears at the end of the **Main** tab of Tasks. The game pays only after Adsgram has called the Reward URL: the "reward" event in the browser alone gets nothing. A completion that is not claimed (limit reached, app closed) expires after 6 hours.

Checking it: `grep webhooks/adsgram-task /var/log/nginx/access.log` shows Adsgram's calls (12 bytes in the log means accepted, 13 means ignored). Ledger reason: `sponsored_task`. In Adsgram's debug/test mode no Reward URL call is sent, so a test task shows but pays nothing: that is expected.
