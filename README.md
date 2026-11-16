# Swiftab Server

Go API behind the Swiftab restaurant dashboard (`swiftab_web`), the diner app
and the waiter app. Data lives in MongoDB; images go to Cloudinary.

## Run

```bash
cp .env.example .env   # fill in MongoDB, JWT secret and Cloudinary
go run ./cmd/api
```

```bash
go test ./...
```

Reservations are created inside a MongoDB transaction, which needs a replica
set (MongoDB Atlas is one). Against a standalone `mongod` the server falls
back to a non-transactional check.

## Configuration

See `.env.example`. Only `MONGODB_CONN`, `JWT_SECRET` and the three Cloudinary
variables are needed; everything else has a default.

Things worth knowing in production:

- **Sessions.** The dashboard signs in with an HTTP-only cookie
  (`access_token`, 7 days). The mobile apps get a bearer token (30 days).
- **CORS.** Only `swiftab.co.ke`, `www.swiftab.co.ke` and the origins in
  `ALLOWED_ORIGIN` may call the API from a browser. Set `APP_ENV=development`
  locally to allow `localhost`.
- **Mobile auth.** User and waiter routes verify a bearer token when one is
  sent and refuse to act on another account's data. Requests with no token are
  still accepted so already-released app builds keep working; set
  `REQUIRE_MOBILE_AUTH=true` after the builds that send tokens are live.
- **Email.** Optional. With SMTP set, waiter invite codes and diner password
  reset codes are emailed. Without it, invite codes appear in the dashboard
  for the manager to pass on, and password reset reports that it is
  unavailable.
- **Push notifications** go through Expo's push service to devices that
  registered a token (`/push-token` routes). No server configuration needed.
- **Live updates.** `GET /api/v2/admin/events` is a Server-Sent Events stream
  of order and reservation changes for the signed-in restaurant. It reaches
  dashboards connected to the same server instance; the dashboard also polls,
  so nothing is missed if you run more than one instance.

## API

All routes are under `/api/v2`. Errors are JSON with the text under both
`error` and `message`.

### Dashboard (admin cookie)

| Area | Routes |
| --- | --- |
| Account | `POST /admin/signup` `POST /admin/login` `POST /admin/logout` `GET /admin/info` `PUT /admin/profile` `PUT /admin/password` |
| Restaurant | `POST /admin/restaurant` (setup, multipart: `data` JSON + `cover` + optional `logo`) · `PUT /admin/restaurant` (settings) |
| Reports | `GET /admin/dashboard?period=weekly\|monthly\|yearly` · `GET /admin/events` |
| Menu | `GET /admin/menu` · `POST/PUT /admin/menu/categories` · `PUT/DELETE /admin/menu/categories/{key}` · `POST /admin/menu/{category}` · `PUT/DELETE /admin/menu/{category}/{itemId}` |
| Floor plan | `GET/POST /admin/layout/tables` (floors + tables + chairs) · `GET/POST /admin/layout/info` · `GET /admin/layout/live` |
| Orders | `GET /admin/orders?status=&from=&to=` · `PUT /admin/orders/{orderId}` |
| Reservations | `GET /admin/reservations` · `PUT /admin/reservations/{id}/status` |
| Staff | `POST /admin/waiters/signup` · `GET /admin/waiters` · `POST /admin/waiters/{id}/invite[?reset=true]` · `DELETE /admin/waiters/{id}` |
| Promotions | `GET/POST /admin/promotions` · `PUT/DELETE /admin/promotions/{id}` |

### Public

`GET /users/restaurants[?q=]` · `GET|POST /users/restaurants/nearby` ·
`GET /restaurants/{id}` · `GET /restaurants/{id}/menu` ·
`GET /restaurants/{id}/menu/sections` · `GET /restaurants/{id}/tables` ·
`POST /reservations/availability` · `GET /users/promotions`

### Diner app (bearer token)

`POST /users/signup` · `POST /users/login` ·
`POST /users/password/forgot|verify|reset` · `PUT /users/{userId}/profile` ·
`POST|DELETE /users/{userId}/push-token` ·
`GET|POST /users/{userId}/recently-viewed` ·
`POST /users/{userId}/restaurants/{restaurantId}/reservations` ·
`GET /users/{userId}/reservations/active|completed|cancelled` ·
`POST /users/{userId}/reservations/cancel` ·
`POST /users/{userId}/restaurants/{restaurantId}/reviews` ·
`POST /users/orders` · `GET /users/{userId}/orders` ·
`PUT /users/orders/{orderId}/complete` (request the bill) ·
`PUT /users/orders/{orderId}/cancel`

### Waiter app (bearer token)

`POST /waiters/verify` · `POST /waiters/password` · `POST /waiters/login` ·
`GET /waiters/restaurants/{restaurantId}/orders?tab=not-taken|served|payment|completed` ·
`GET /waiters/restaurants/{restaurantId}/floor` ·
`PUT /waiters/orders/{orderId}/status` · `PUT /waiters/orders/{orderId}/complete` ·
`GET /waiters/{waiterId}/summary` · `PUT /waiters/{waiterId}/profile` ·
`PUT /waiters/{waiterId}/duty` · `POST|DELETE /waiters/{waiterId}/push-token`

## Order lifecycle

`placed` → `served` (a waiter claims it) → `ready_to_pay` (the diner asks for
the bill and names a payment method) → `completed` (the waiter confirms
payment; the reservation is completed too). `cancelled` can happen before
payment. Prices always come from the restaurant's menu, never from the client.

Payment methods are stored as `mpesa`, `cash`, `card` or `airtel_money`
whatever label the client sends.
