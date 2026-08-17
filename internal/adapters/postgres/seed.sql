-- Demo data, applied only when SEED_DEMO_DATA=true. Deliberately kept out of the
-- migration set so a production database never receives it.
-- Fixed UUIDs keep this idempotent across restarts.

INSERT INTO halls (id, name) VALUES
    ('11111111-1111-1111-1111-111111111111', 'Hall A'),
    ('22222222-2222-2222-2222-222222222222', 'Hall B')
ON CONFLICT (id) DO NOTHING;

-- Hall A: rows A-E, 8 seats each. Back two rows are premium.
INSERT INTO seats (hall_id, row_label, seat_number, seat_class)
SELECT '11111111-1111-1111-1111-111111111111',
       chr(64 + r),
       n,
       CASE WHEN r >= 4 THEN 'premium' ELSE 'standard' END
FROM generate_series(1, 5) AS r, generate_series(1, 8) AS n
ON CONFLICT (hall_id, row_label, seat_number) DO NOTHING;

-- Hall B: rows A-D, 6 seats each.
INSERT INTO seats (hall_id, row_label, seat_number, seat_class)
SELECT '22222222-2222-2222-2222-222222222222',
       chr(64 + r),
       n,
       CASE WHEN r = 1 AND n <= 2 THEN 'accessible' ELSE 'standard' END
FROM generate_series(1, 4) AS r, generate_series(1, 6) AS n
ON CONFLICT (hall_id, row_label, seat_number) DO NOTHING;

INSERT INTO movies (id, slug, title, description, duration_minutes, poster_url) VALUES
    ('aaaaaaaa-0000-4000-8000-000000000001', 'inception', 'Inception',
     'A thief who steals corporate secrets through dream-sharing technology.', 148, ''),
    ('aaaaaaaa-0000-4000-8000-000000000002', 'dune-part-two', 'Dune: Part Two',
     'Paul Atreides unites with the Fremen to wage war against House Harkonnen.', 166, ''),
    ('aaaaaaaa-0000-4000-8000-000000000003', 'the-batman', 'The Batman',
     'Batman ventures into Gotham''s underworld when a killer leaves behind a trail of cryptic clues.', 176, '')
ON CONFLICT (id) DO NOTHING;

-- Showtimes are anchored to the current day so the demo always has future screenings.
INSERT INTO showtimes (id, movie_id, hall_id, starts_at, base_price_cents, currency) VALUES
    ('bbbbbbbb-0000-4000-8000-000000000001', 'aaaaaaaa-0000-4000-8000-000000000001',
     '11111111-1111-1111-1111-111111111111', date_trunc('day', now()) + interval '18 hours', 1200, 'USD'),
    ('bbbbbbbb-0000-4000-8000-000000000002', 'aaaaaaaa-0000-4000-8000-000000000001',
     '11111111-1111-1111-1111-111111111111', date_trunc('day', now()) + interval '21 hours', 1400, 'USD'),
    ('bbbbbbbb-0000-4000-8000-000000000003', 'aaaaaaaa-0000-4000-8000-000000000002',
     '22222222-2222-2222-2222-222222222222', date_trunc('day', now()) + interval '19 hours', 1500, 'USD'),
    ('bbbbbbbb-0000-4000-8000-000000000004', 'aaaaaaaa-0000-4000-8000-000000000002',
     '11111111-1111-1111-1111-111111111111', date_trunc('day', now()) + interval '1 day 18 hours', 1500, 'USD'),
    ('bbbbbbbb-0000-4000-8000-000000000005', 'aaaaaaaa-0000-4000-8000-000000000003',
     '22222222-2222-2222-2222-222222222222', date_trunc('day', now()) + interval '1 day 20 hours', 1300, 'USD')
ON CONFLICT (id) DO NOTHING;
