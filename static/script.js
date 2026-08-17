// Demo UI for the booking API. It speaks the same endpoints a real client
// would: browse the catalog, hold seats, start checkout, then let the mock
// gateway call back through the webhook path.
(function () {
    const API = "/api/v1";

    // The user id stands in for a signed-in subject; the API reads it from the
    // X-User-Id header. Keeping it in localStorage means a reload keeps your
    // bookings instead of turning you into a stranger.
    const userID = (function () {
        let id = localStorage.getItem("cinema_user_id");
        if (!id) {
            id = "user_" + crypto.randomUUID().replace(/-/g, "").slice(0, 10);
            localStorage.setItem("cinema_user_id", id);
        }
        return id;
    })();
    document.getElementById("userBadge").textContent = userID;

    let movies = [];
    let selectedMovie = null;
    let selectedShowtime = null;
    let selectedSeats = []; // seat ids picked but not yet held
    let booking = null; // the active held/confirmed booking
    let payment = null;
    let pollInterval = null;
    let timerInterval = null;

    // --- API helpers ---

    function api(method, path, body, extraHeaders) {
        const headers = { "X-User-Id": userID };
        if (body) headers["Content-Type"] = "application/json";
        Object.assign(headers, extraHeaders || {});

        const opts = { method: method, headers: headers };
        if (body) opts.body = JSON.stringify(body);

        return fetch(API + path, opts).then(function (r) {
            if (r.status === 204) return null;
            return r.text().then(function (text) {
                let data = null;
                if (text) {
                    try {
                        data = JSON.parse(text);
                    } catch (e) {
                        data = null;
                    }
                }
                if (!r.ok) {
                    const message =
                        (data && data.error && data.error.message) ||
                        "request failed (" + r.status + ")";
                    throw new Error(message);
                }
                return data;
            });
        });
    }

    function money(cents, currency) {
        return (cents / 100).toFixed(2) + " " + (currency || "USD");
    }

    // --- Movies ---

    function loadMovies() {
        api("GET", "/movies")
            .then(function (data) {
                movies = data.movies || [];
                renderMovies();
            })
            .catch(function (err) {
                document.getElementById("movieList").innerHTML =
                    '<div class="empty-state">' +
                    escapeHtml(err.message) +
                    "</div>";
            });
    }

    function renderMovies() {
        const container = document.getElementById("movieList");
        container.innerHTML = "";
        if (movies.length === 0) {
            container.innerHTML =
                '<div class="empty-state">No movies available</div>';
            return;
        }
        movies.forEach(function (m) {
            const card = document.createElement("div");
            card.className =
                "movie-card" +
                (selectedMovie && selectedMovie.id === m.id ? " selected" : "");
            const title = document.createElement("h3");
            title.textContent = m.title;
            const meta = document.createElement("p");
            meta.textContent = m.duration_minutes + " min";
            card.appendChild(title);
            card.appendChild(meta);
            card.addEventListener("click", function () {
                selectMovie(m);
            });
            container.appendChild(card);
        });
    }

    function selectMovie(movie) {
        selectedMovie = movie;
        selectedShowtime = null;
        selectedSeats = [];
        stopPolling();
        renderMovies();
        document.getElementById("mainContent").style.display = "none";
        document.getElementById("checkoutArea").innerHTML = "";
        document.getElementById("showtimeSection").style.display = "block";
        loadShowtimes();
    }

    // --- Showtimes ---

    function loadShowtimes() {
        api("GET", "/movies/" + selectedMovie.id + "/showtimes").then(
            function (data) {
                renderShowtimes(data.showtimes || []);
            },
        );
    }

    function renderShowtimes(showtimes) {
        const container = document.getElementById("showtimeList");
        container.innerHTML = "";
        if (showtimes.length === 0) {
            container.innerHTML =
                '<div class="empty-state">No upcoming showtimes</div>';
            return;
        }
        showtimes.forEach(function (s) {
            const card = document.createElement("div");
            card.className =
                "showtime-card" +
                (selectedShowtime && selectedShowtime.id === s.id
                    ? " selected"
                    : "");
            const when = document.createElement("strong");
            when.textContent = new Date(s.starts_at).toLocaleString();
            const meta = document.createElement("p");
            meta.textContent =
                s.hall_name + " · from " + money(s.base_price_cents, s.currency);
            card.appendChild(when);
            card.appendChild(meta);
            card.addEventListener("click", function () {
                selectShowtime(s);
            });
            container.appendChild(card);
        });
    }

    function selectShowtime(showtime) {
        selectedShowtime = showtime;
        selectedSeats = [];
        booking = null;
        payment = null;
        clearTimer();
        loadShowtimes(); // re-render so the picked showtime is highlighted
        document.getElementById("mainContent").style.display = "flex";
        renderSidePanel();
        fetchSeats();
        startPolling();
    }

    // --- Seat map ---

    function fetchSeats() {
        if (!selectedShowtime) return;
        api("GET", "/showtimes/" + selectedShowtime.id + "/seats")
            .then(function (data) {
                renderGrid(data.seats || []);
            })
            .catch(function () {
                /* a transient poll failure is not worth a banner */
            });
    }

    function renderGrid(seats) {
        const grid = document.getElementById("seatGrid");
        grid.innerHTML = "";

        // Group by row so the map reads like the auditorium.
        const rows = {};
        const order = [];
        seats.forEach(function (s) {
            if (!rows[s.row_label]) {
                rows[s.row_label] = [];
                order.push(s.row_label);
            }
            rows[s.row_label].push(s);
        });

        order.forEach(function (label) {
            const rowDiv = document.createElement("div");
            rowDiv.className = "seat-row";

            const left = document.createElement("div");
            left.className = "row-label";
            left.textContent = label;
            rowDiv.appendChild(left);

            rows[label].forEach(function (seat) {
                rowDiv.appendChild(seatButton(seat));
            });

            const right = document.createElement("div");
            right.className = "row-label";
            right.textContent = label;
            rowDiv.appendChild(right);

            grid.appendChild(rowDiv);
        });
    }

    function seatButton(seat) {
        const btn = document.createElement("button");
        btn.className = "seat";
        btn.textContent = seat.seat_number;
        btn.title =
            seat.row_label +
            seat.seat_number +
            " · " +
            seat.seat_class +
            " · " +
            money(seat.price_cents, selectedShowtime.currency);

        if (seat.status === "sold") {
            btn.classList.add("seat--confirmed");
        } else if (seat.status === "held" && seat.mine) {
            btn.classList.add("seat--held-mine");
        } else if (seat.status === "held") {
            btn.classList.add("seat--held-other");
        } else if (selectedSeats.indexOf(seat.id) !== -1) {
            btn.classList.add("seat--selected");
        }

        const selectable = seat.status === "available" && !booking;
        if (!selectable) {
            btn.disabled = true;
        } else {
            btn.addEventListener("click", function () {
                toggleSeat(seat);
            });
        }
        return btn;
    }

    function toggleSeat(seat) {
        const i = selectedSeats.indexOf(seat.id);
        if (i === -1) {
            selectedSeats.push(seat.id);
        } else {
            selectedSeats.splice(i, 1);
        }
        fetchSeats();
        renderSidePanel();
    }

    // --- Booking ---

    function holdSeats() {
        if (selectedSeats.length === 0) return;
        api(
            "POST",
            "/bookings",
            {
                showtime_id: selectedShowtime.id,
                seat_ids: selectedSeats,
            },
            // A retried click must not reserve a second set of seats.
            { "Idempotency-Key": crypto.randomUUID() },
        )
            .then(function (data) {
                booking = data;
                payment = null;
                selectedSeats = [];
                startTimer();
                fetchSeats();
                renderSidePanel();
            })
            .catch(function (err) {
                renderSidePanel(err.message);
                fetchSeats();
            });
    }

    function releaseBooking() {
        if (!booking) return;
        api("DELETE", "/bookings/" + booking.id)
            .then(function () {
                booking = null;
                payment = null;
                clearTimer();
                fetchSeats();
                renderSidePanel("Booking released");
            })
            .catch(function (err) {
                renderSidePanel(err.message);
            });
    }

    // --- Checkout ---

    function startCheckout() {
        if (!booking) return;
        api("POST", "/bookings/" + booking.id + "/checkout")
            .then(function (data) {
                payment = data;
                renderSidePanel();
            })
            .catch(function (err) {
                renderSidePanel(err.message);
            });
    }

    // simulatePayment stands in for the customer finishing on the gateway's
    // page: the server builds a signed callback and feeds it through the very
    // webhook path a real provider would use.
    function simulatePayment(outcome) {
        if (!payment) return;
        api("POST", "/payments/" + payment.id + "/simulate", {
            outcome: outcome,
        })
            .then(function (data) {
                payment = data;
                return api("GET", "/bookings/" + booking.id);
            })
            .then(function (data) {
                booking = data;
                if (booking.status === "confirmed") clearTimer();
                fetchSeats();
                renderSidePanel();
            })
            .catch(function (err) {
                renderSidePanel(err.message);
            });
    }

    function reset() {
        booking = null;
        payment = null;
        selectedSeats = [];
        clearTimer();
        fetchSeats();
        renderSidePanel();
    }

    // --- Side panel ---

    function renderSidePanel(message) {
        const area = document.getElementById("checkoutArea");
        area.innerHTML = "";

        const panel = document.createElement("div");
        panel.className = "checkout";

        if (!booking) {
            panel.appendChild(heading("Selection"));
            panel.appendChild(
                info("Seats", selectedSeats.length ? selectedSeats.length : "—"),
            );
            panel.appendChild(
                button(
                    "Hold seats",
                    "btn--confirm",
                    holdSeats,
                    selectedSeats.length === 0,
                ),
            );
        } else {
            panel.appendChild(heading("Booking " + booking.reference));
            panel.appendChild(info("Status", booking.status));
            panel.appendChild(
                info(
                    "Seats",
                    booking.seats
                        .map(function (s) {
                            return s.row_label + s.seat_number;
                        })
                        .join(", "),
                ),
            );
            panel.appendChild(
                info(
                    "Total",
                    money(booking.total_amount_cents, booking.currency),
                ),
            );

            if (booking.status === "held") {
                const timer = document.createElement("div");
                timer.className = "timer";
                timer.id = "timer";
                timer.textContent = "--:--";
                panel.appendChild(timer);

                const buttons = document.createElement("div");
                buttons.className = "checkout-buttons";
                if (!payment) {
                    buttons.appendChild(
                        button("Pay", "btn--confirm", startCheckout),
                    );
                } else {
                    buttons.appendChild(
                        button("Complete", "btn--confirm", function () {
                            simulatePayment("success");
                        }),
                    );
                    buttons.appendChild(
                        button("Decline", "btn--release", function () {
                            simulatePayment("failure");
                        }),
                    );
                }
                buttons.appendChild(
                    button("Release", "btn--release", releaseBooking),
                );
                panel.appendChild(buttons);

                if (payment) {
                    panel.appendChild(info("Payment", payment.status));
                }
            } else {
                const buttons = document.createElement("div");
                buttons.className = "checkout-buttons";
                if (booking.status === "confirmed") {
                    buttons.appendChild(
                        button("Cancel & refund", "btn--release", releaseBooking),
                    );
                }
                buttons.appendChild(button("New booking", "btn--confirm", reset));
                panel.appendChild(buttons);
            }
        }

        if (message) {
            const status = document.createElement("div");
            status.className =
                "status-msg " +
                (booking && booking.status === "confirmed" ? "success" : "error");
            status.textContent = message;
            panel.appendChild(status);
        }

        area.appendChild(panel);
        updateTimer();
    }

    function heading(text) {
        const h = document.createElement("h3");
        h.textContent = text;
        return h;
    }

    function info(label, value) {
        const div = document.createElement("div");
        div.className = "checkout-info";
        const span = document.createElement("span");
        span.textContent = label + ": ";
        div.appendChild(span);
        div.appendChild(document.createTextNode(String(value)));
        return div;
    }

    function button(text, variant, onClick, disabled) {
        const btn = document.createElement("button");
        btn.className = "btn " + variant;
        btn.textContent = text;
        btn.disabled = !!disabled;
        btn.addEventListener("click", onClick);
        return btn;
    }

    // --- Hold timer ---

    function startTimer() {
        clearTimer();
        updateTimer();
        timerInterval = setInterval(updateTimer, 1000);
    }

    function updateTimer() {
        const el = document.getElementById("timer");
        if (!el || !booking || !booking.expires_at) return;

        const remaining = Math.max(
            0,
            Math.floor((new Date(booking.expires_at) - Date.now()) / 1000),
        );
        const mins = Math.floor(remaining / 60);
        const secs = remaining % 60;
        el.textContent =
            String(mins).padStart(2, "0") + ":" + String(secs).padStart(2, "0");
        el.classList.toggle("urgent", remaining < 60);

        if (remaining <= 0) {
            clearTimer();
            booking = null;
            payment = null;
            fetchSeats();
            renderSidePanel("Hold expired");
        }
    }

    function clearTimer() {
        if (timerInterval) {
            clearInterval(timerInterval);
            timerInterval = null;
        }
    }

    // --- Polling: other people's holds show up without a reload ---

    function startPolling() {
        stopPolling();
        pollInterval = setInterval(fetchSeats, 3000);
    }

    function stopPolling() {
        if (pollInterval) {
            clearInterval(pollInterval);
            pollInterval = null;
        }
    }

    function escapeHtml(str) {
        const div = document.createElement("div");
        div.textContent = str;
        return div.innerHTML;
    }

    loadMovies();
})();
