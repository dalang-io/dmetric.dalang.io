# DMetric

Dashboard diagnosis untuk menjawab satu pertanyaan: **kenapa server ini
lemot?** DMetric menampilkan CPU, RAM, penyimpanan, internet (RX/TX), disk I/O
(baca/tulis, latensi, antrean), port terbuka, proses, dan aplikasi fokus —
dalam satu tampilan terang, dengan panel diagnosis yang menyebut tersangka
utama dan langkah berikutnya.

- **Desktop**: jendela MyGo (WKWebView di macOS, WebView2 di Windows,
  WebKitGTK di Linux) yang memuat UI yang sama dengan web app.
- **Web app**: `http://127.0.0.1:8154` (port dapat diubah), tema terang (light mode).
- **Portal**: satu binary, tanpa cgo, untuk macOS/Linux/Windows × amd64/arm64.

## Untuk kasus Windows Server + Chromeleon

Dashboard ini **bukan Task Manager kedua**. Task Manager menunjukkan angka
sesaat; DMetric menyimpan riwayat, mengukur latensi/antrean (bukan cuma
utilisasi), memisahkan volume OS dan data, dan mengatribusikan beban ke
Chromeleon/SQL Server. Alur yang disarankan:

1. Jalankan di server: `dmetric.exe -no-window -host 0.0.0.0`.
2. Buka `http://<ip-server>:8154` **dari PC client yang lemot**. Angka
   “Latensi browser→server” dan panel diagnosis akan menunjukkan apakah jalur
   client→server yang lambat.
3. Saat user mengeluh lemot, klik **Catat insiden**. File JSON berisi snapshot,
   diagnosis, riwayat 15 menit, alert, dan pengaturan — simpan untuk dibandingkan.
4. Baca panel **Diagnosis**:
   - **Disk lambat / antrean tinggi** → lihat tabel **Volume & RAID**. Bandingkan
     `C:` (RAID 1 SSD, OS) dengan `D:`/volume data (RAID 5, 3× HDD SAS). Latensi
     tulis tinggi hanya di volume data = RAID 5/HDD, cache baterai RAID, rebuild,
     antivirus, atau antrean SQL Server.
   - **RAM tinggi** → cek `sqlservr` (max memory SQL), proses Chromeleon, pagefile.
   - **CPU/load tinggi** → cek proses teratas, reporting/processing Chromeleon,
     query SQL.
   - **Jaringan lambat/loss** → cek probe ke SQL/client, switch, NIC teaming, VPN.
   - **Tidak ada saturasi** → resource server normal; tersangka berikutnya adalah
     aplikasi (SQL waits, lock, queue Chromeleon) atau jalur jaringan client.
5. Pilih target jaringan di Pengaturan: tambahkan `host:port` SQL/PC client
   (mis. `10.0.0.20:1433, 10.0.0.50:443`) atau ambil dari port lokal; probe
   berlaku langsung. Pilih juga interface yang menghadap client agar angka
   Internet tidak tercampur iSCSI/backup.

### Checklist Chromeleon / HPE

- **RAID 5 data**: tulis acak pada HDD punya penalti read-modify-write. Latensi
  tulis >20 ms dengan antrean naik adalah sinyal array, bukan CPU. Cek cache
  battery/capacitor dan status degraded/rebuild di iLO/SSA.
- **RAID 1 OS SSD**: jika latency tinggi di `C:` juga, masalahnya bukan RAID 5;
  curigai antivirus, driver/controller, atau firmware.
- **SQL Server**: batasi *max server memory* agar OS tidak kehabisan RAM; cek
  wait stats, backup yang tumpang tindih, dan penempatan data/log.
- **Antivirus/Defender**: kecualikan database, log, temp Chromeleon, dan file
  SQL; real-time scan pada volume data sangat terasa di RAID 5.
- **Jaringan**: probe dari PC client; latensi browser→server >100 ms atau loss
  TCP menunjuk jalur client/server, bukan CPU.
- **Windows/HPE**: power plan High Performance, pagefile, NIC teaming, firmware
  HPE, dan event log/IML untuk error disk/controller.

## Stack

| Bagian | Teknologi |
|---|---|
| HTTP server, API, JSON, embed aset | Go standard library |
| Jendela desktop | [MyGo](https://github.com/egoist/mygo) — pure Go, tanpa cgo |
| Penyimpanan sampel & alert | SQLite via `modernc.org/sqlite` — pure Go, tanpa cgo |
| Pembacaan metrik OS | `github.com/shirou/gopsutil/v4` — adapter lintas OS, tanpa cgo |

`gopsutil` dipakai sebagai adapter pembacaan metrik agar satu kode bekerja di
macOS, Linux, dan Windows (`/proc`, sysctl, API Windows) tanpa syscall buatan
sendiri yang tidak teruji. Server, API, dan UI memakai standard library.

## Jalankan

```sh
# Desktop + web app (port 8154)
go run .

# Web app saja, tanpa jendela (cocok untuk server/headless)
go run . -no-window

# Port lain
go run . -port 9000

# Agar bisa dibuka dari PC client / LAN (tanpa autentikasi — baca Keamanan)
go run . -host 0.0.0.0 -no-window
```

Buka `http://127.0.0.1:8154`. Jendela desktop memuat URL yang sama. Di
Windows Server, izinkan port dari PC client:

```powershell
New-NetFirewallRule -DisplayName "DMetric" -Direction Inbound -Protocol TCP -LocalPort 8154 -Action Allow
```

Untuk pemantauan permanen, jalankan `dmetric.exe -no-window -host 0.0.0.0`
sebagai Scheduled Task/service. **Windows Server 2019 tidak menyertakan
WebView2**, jadi mode `-no-window` + browser dari PC client adalah mode utama;
jika ingin jendela desktop di server, instal Evergreen WebView2 Runtime.
Jalankan sebagai **Administrator** agar counter disk/per-proses tidak ditolak,
dan aktifkan counter dengan `diskperf -y` bila perlu. Kecualikan database,
log, dan temp Chromeleon/SQL dari antivirus real-time.

### Flag dan environment

| Flag | Environment | Bawaan |
|---|---|---|
| `-port` | `DMETRIC_PORT` | `8154` |
| `-host` | `DMETRIC_HOST` | `127.0.0.1` |
| `-interval` | `DMETRIC_INTERVAL` | `2` detik |
| `-data` | `DMETRIC_DATA` | direktori konfigurasi pengguna + `/DMetric` |
| `-update-repo` | `DMETRIC_UPDATE_REPO` | `dalang-io/dmetric.dalang.io` |
| `-no-window` | — | jendela desktop aktif |
| `-version` | — | cetak versi |

Urutan prioritas port: flag → environment → pengaturan tersimpan → `8154`.
Port/interval yang diubah dari halaman Pengaturan disimpan ke SQLite dan
berlaku setelah restart. Ambang alert, aplikasi fokus, probe jaringan, dan
pilihan interface berlaku langsung. Flag/environment mengunci port/interval.

Data: `dmetric.db` (SQLite, WAL). Sampel 7 hari, alert 30 hari.

## Prasyarat

Tombol **Prasyarat** di header memeriksa agar aplikasi berjalan lancar:
direktori data dapat ditulis, ruang disk, hak administrator/root, WebView
desktop, PowerShell/CIM, counter disk Windows, akses GitHub untuk update, dan
tool hardware opsional (smartctl/nvme/ssacli/ipmitool). Setiap temuan berisi
tindakan perbaikan; ini bukan pengganti dokumentasi instalasi.

## Fitur diagnosis

- **Panel Diagnosis**: peringkat tersangka dari sampel terakhir + 2 menit
  terakhir, dengan pembanding baseline p50/p95 **1 jam** (“di atas kebiasaan”
  bila berbeda dari pola normal server). Level Normal/Waspada/Kritis, detail
  angka, proses teratas, dan tindakan berikutnya. Serve normal adalah
  diagnosis yang sah.
- **Volume & RAID**: setiap volume dengan pemakaian, baca/tulis, latensi R/W,
  dan antrean. Di Windows volume `C:`, `D:`, … dipetakan langsung ke counter
  disk; di Linux perangkat/partisi dipetakan ke `/proc/diskstats`.
- **Disk**: throughput + IOPS + latensi + antrean agregat; throughput tinggi
  dengan latensi normal tidak dianggap bottleneck.
- **Aplikasi fokus**: searchable multi-select dengan chip. Cari/pilih beberapa
  proses berjalan, pilih preset (Chromeleon+SQL, SQL, web, Docker/WSL), atau
  ketik pola kustom; maksimal 8 pola.
- **Proses**: top pemakai CPU, RAM, **baca disk, dan tulis disk** — proses yang
  menghabiskan disk tetapi CPU/RAM-nya kecil tetap muncul.
- **Jaringan**: latensi ICMP gateway, TCP handshake ke target `host:port`
  lewat searchable multi-select (cari port lokal atau ketik host:port kustom),
  loss setelah ≥3 percobaan, plus **latensi browser→server**. Pilih interface
  yang dijumlahkan; default semua non-loopback. Koneksi ditolak = “port tertutup”, bukan loss. RX/TX
  hanya agregat sistem; **tidak ada RX/TX per proses** (butuh ETW/nettop).
- **Kesehatan hardware**: motherboard/BIOS, CPU (model, serial/ID, core/thread,
  clock, suhu), tabel modul RAM (slot/merek/serial/part/ukuran/speed, ECC,
  WHEA), GPU/NPU, fan, PSU, dan disk fisik (health, suhu, wear, power-on hours,
  error, **serial**) — diperbarui tiap 60 detik. Lihat bagian di bawah.
- **Catat insiden**: mengunduh JSON berisi ringkasan teks, snapshot, diagnosis,
  riwayat 15 menit, alert, pengaturan, dan hardware.
- **Tren**: 15 menit, 1 jam, 6 jam, 24 jam, termasuk tren latensi disk
  (baca/tulis) agar stall RAID 5 yang intermiten terlihat.
- **Alert**: ambang dengan durasi sustain/recovery, aktif + riwayat + ack.
- **SSE** (`/api/stream`) 2 detik; fallback polling bila koneksi putus.

### Kesehatan hardware

DMetric memeriksa hardware langsung, bukan menyalin dcheck (yang ditulis Rust
dan hanya Linux/FreeBSD/macOS — tidak berjalan di Windows Server 2019):

| Platform | Sumber | Isi |
|---|---|---|
| Windows | CIM/PowerShell (`Win32_Processor`/`VideoController`/`Fan`/`PowerSupply`, `Get-PhysicalDisk`, `Get-StorageReliabilityCounter`, WHEA), SMART ATA bawaan (`IOCTL_ATA_PASS_THROUGH`), opsional `ssacli` dan `ipmitool` | board/BIOS/serial, CPU (ID/suhu ACPI), DIMM, GPU/NPU, fan, PSU, disk dengan ID stabil (`UniqueId`), health/wear/temp/POH/errors, atribut SMART |
| Linux | sysfs DMI, `/proc`, hwmon, EDAC, dmidecode (root), lspci, **SMART ATA bawaan (SG_IO)**, `nvme-cli`, opsional `smartctl`, `ssacli`, `ipmitool` | board/BIOS, CPU serial/temp, DIMM, GPU/NPU, fan, PSU, ECC CE/UE, disk dengan ID stabil (WWN/by-id), tabel SMART lengkap |
| macOS | `system_profiler` (`SPHardware`, `SPMemory`, `SPStorage`, `SPNVMe`, `SPSerialATA`, `SPDisplays`) | hardware overview, RAM, GPU (merek/ukuran), disk SMART ringkas |

Poin utama:

- **Disk diidentifikasi dengan ID stabil** (WWN / by-id di Linux, `UniqueId`
  di Windows), bukan `sda`/`sdb` yang berubah tiap boot.
- **Hanya disk fisik.** Ceph RBD, loop, LVM/md, dan perangkat virtual lain tidak
  ditampilkan dan tidak ikut dijumlahkan ke I/O disk.
- **Tabel SMART lengkap**: klik nama disk untuk melihat semua atribut (nilai,
  worst, ambang, mentah, status); NVMe menampilkan seluruh log kesehatan.
- **SMART tanpa smartmontools**: dibaca langsung oleh DMetric (butuh root /
  Administrator). Bila health tidak terbaca, UI menampilkan "Tidak diketahui"
  beserta alasannya, tidak pernah diam-diam "Normal".
- **HPE Smart Array** lewat `ssacli`: drive fisik di balik RAID, status logical
  drive (degraded/rebuilding), serta status controller, cache, dan baterai.

Status **Normal/Waspada/Kritis** didasarkan pada health controller, wear ≥80/90%,
suhu disk ≥60/70°C, suhu CPU ≥85/95°C, ECC uncorrectable, WHEA error, dan error
baca/tulis. Temuan hardware masuk ke panel diagnosis dengan prioritas di atas
saturasi resource: disk yang gagal atau array degraded adalah penyebab, bukan
gejala.

> **Status verifikasi.** Linux (SATA, NVMe, filter RBD, ID stabil) sudah
> diuji di perangkat keras nyata. **Windows dan `ssacli` belum pernah dijalankan
> di perangkat keras nyata**: kodenya terkompilasi dan parsernya diuji dengan
> fixture buatan. Detail, batas, dan cara menyumbang output perangkat keras
> nyata ada di [docs/hardware-health.md](docs/hardware-health.md).

Batas lain:

- Di Windows, jalankan sebagai Administrator; PowerShell/CIM bisa dibatasi
  kebijakan. Storage counter kosong akan memunculkan peringatan.
- ECC di Linux dari EDAC (butuh driver/root); Windows tidak memberi ECC counter
  lewat CIM — kolom ECC tampil `—`. WHEA error 7 hari adalah sinyal pengganti.
- Suhu CPU: Linux hwmon/thermal; Windows dari ACPI thermal zone (bisa suhu
  board, bukan core); macOS tidak mengekspos. GPU/NPU: Windows
  `Win32_VideoController`/`Get-PnpDevice`, Linux `lspci`, macOS
  `SPDisplaysDataType`.
- Fan/PSU: Windows `Win32_Fan`/`Win32_PowerSupply` bila diisi vendor, Linux
  hwmon dan `ipmitool`; HPE ProLiant biasanya perlu iLO/`ipmitool` (driver
  IPMI, root). macOS tidak mengekspos fan/PSU. Tanpa data, baris disembunyikan
  dan alasannya ada di notes.
- Drive di belakang RAID non-HPE (mis. Dell PERC) belum terbaca; virtual
  disk-nya diberi label "RAID virtual". `smartctl -d megaraid,N` dipakai bila
  smartctl terpasang (belum teruji di perangkat keras).
- dcheck punya fitur di luar monitoring (fake-capacity test, recovery/undelete,
  SMART self-test/bench) yang **tidak** dimasukkan ke dashboard realtime.

## Ambang alert bawaan

| Metrik | Bawaan |
|---|---|
| CPU sistem | 85% |
| RAM sistem | 85% |
| Penyimpanan | 90% |
| Baca disk / tulis disk | 100 MB/s |
| Unduh / unggah jaringan | 25 / 10 MB/s |
| Load per core (perlambatan) | 1,5 |
| CPU satu proses | 80% |
| RAM satu proses | 4096 MB |
| Durasi pelanggaran | 15 detik |
| Durasi pemulihan | 20 detik |

Alert menjadi **Kritis** pada ≥95% untuk metrik persentase (atau 1,25× ambang),
dan 1,25× ambang untuk throughput. Lonjakan sesaat tidak memicu alert.

### Ambang diagnosis (tetap)

| Sinyal | Waspada | Kritis |
|---|---|---|
| Latensi disk baca/tulis | ≥20 ms | ≥50 ms |
| Antrean disk | ≥2 | ≥8 |
| RTT probe TCP | ≥100 ms | ≥300 ms |
| Loss probe TCP | ≥1% | ≥5% |

Diagnosis hanya memakai latensi/antrean disk untuk menuduh storage; throughput
tinggi dengan latensi normal diberi catatan informasi, bukan tersangka.

## Pembaruan

Diatur di **Pengaturan → Pembaruan**:

| Mode | Perilaku |
|---|---|
| **Manual** (bawaan) | tidak menghubungi GitHub sendiri; tombol **Update** di header memeriksa dan memasang saat diklik |
| **Cek otomatis** | memeriksa sekali setelah startup lalu tiap *N* jam (bawaan **24 jam**); hanya memberi tahu |
| **Pasang otomatis** | seperti di atas, lalu mengunduh, memverifikasi SHA256, memasang, dan restart |

Pengaman pemasangan otomatis: SHA256 **wajib**; versi yang sudah dicoba tidak
diulang dalam 24 jam; binary lama disimpan sebagai `<binary>.prev`; tidak
berjalan untuk `go run` maupun app macOS `.app` (hanya memeriksa); dan
pengaturannya **hanya bisa diubah dari localhost**, sama seperti tombol Update.
Versi memakai **CalVer**, mis. `v2026.11.0` (tag `vYYYY.M.P`). Repo/fork lain:
`-update-repo owner/name` atau `DMETRIC_UPDATE_REPO`. Selengkapnya (macOS,
Windows, rollback, API) di [docs/updates.md](docs/updates.md).

## Versi dan catatan rilis

Versi tag (`v2026.11.0`) tampil di samping nama di header dan di footer;
klik untuk membuka halaman **Catatan rilis** (`/releases.html`). Halaman itu
membaca `internal/server/ui/releases.json` yang ikut di-embed ke binary, jadi
tetap bisa dibuka di server tanpa internet, dan menambahkan catatan rilis
terbaru dari GitHub bila server bisa menjangkaunya. Berkas yang sama menjadi
teks rilis GitHub. Cara membuat rilis: [docs/releasing.md](docs/releasing.md).

## Build

```sh
make test     # unit test
make cross    # binary darwin/linux/windows × amd64/arm64 di dist/
make build    # paket desktop platform ini lewat MyGo CLI (.app + .dmg di macOS)
make icon     # gambar ulang resources/icon.png
make release  # rilis manual: 6 binary + DMG macOS arm64/Intel + SHA256SUMS + catatan
```

`make release` tidak memakai CI dan tidak mengunggah apa pun; hasilnya ada di
`dist/release/v<versi>/` (lihat [docs/releasing.md](docs/releasing.md)).

`make cross` menghasilkan:

```
dist/dmetric-darwin-arm64      dist/dmetric-darwin-amd64
dist/dmetric-linux-arm64       dist/dmetric-linux-amd64
dist/dmetric-windows-arm64.exe dist/dmetric-windows-amd64.exe
```

Aplikasi pure Go (tanpa cgo), jadi setiap target dapat dibangun dari mesin mana
pun. Konfigurasi paket desktop ada di `mygo.json`; `go tool mygo build` membuat
`.app`/`.dmg` di macOS, installer di Windows, dan paket Linux sesuai platform.
DMG macOS memakai *ad-hoc signature*; di Mac lain Gatekeeper meminta konfirmasi
saat pertama dibuka. Kebutuhan runtime desktop: WKWebView (macOS), WebView2 (Windows), GTK 3 +
WebKitGTK 4.1 (Linux). Mode `-no-window` tidak membutuhkan keduanya.

## API

| Endpoint | Keterangan |
|---|---|
| `GET /api/snapshot` | metrik, hardware, diagnosis, aplikasi fokus, probe, interface, alert aktif |
| `GET /api/candidates` | saran proses, port lokal, interface, gateway untuk picker |
| `GET /api/stream` | SSE, satu snapshot per interval |
| `GET /api/history?minutes=30` | titik tren (maks. 900 titik) |
| `GET /api/alerts?limit=100` | riwayat alert |
| `POST /api/alerts/{id}/ack` | tandai alert dibaca |
| `GET /api/settings` | pengaturan, port efektif, status restart |
| `PUT /api/settings` | simpan port, interval, ambang, aplikasi fokus, probe, interface, dan kebijakan `update` (yang terakhir hanya dari localhost) |
| `GET /api/health` | status server/versi; dipakai mengukur RTT browser |
| `GET /api/update?refresh=1` | cek rilis GitHub terbaru + status pembaruan otomatis (`auto`) |
| `POST /api/update/install` | unduh/pasang update; hanya dari localhost |

Contoh `PUT /api/settings`:

```json
{
  "port": 8154,
  "interval_seconds": 2,
  "thresholds": { "cpu_percent": 85, "memory_percent": 85, "storage_percent": 90,
    "disk_read_mbps": 100, "disk_write_mbps": 100, "net_rx_mbps": 25,
    "net_tx_mbps": 10, "load_per_core": 1.5, "process_cpu_percent": 80,
    "process_memory_mb": 4096, "sustain_seconds": 15, "recovery_seconds": 20 },
  "watch_apps": ["chromeleon", "sqlservr"],
  "network_probes": ["10.0.0.20:1433", "10.0.0.50:443"],
  "network_interfaces": ["Ethernet", "Ethernet 2"]
}
```

## Catatan platform

- **Latensi/antrean disk** berasal dari `disk.IOCounters` gopsutil (Linux
  `/proc/diskstats`, Windows `IOCTL_DISK_PERFORMANCE` per volume, macOS
  IOKit). Windows melaporkan per huruf drive — cocok untuk memisahkan RAID 1
  OS dan RAID 5 data. Kedalaman antrean tidak diekspos gopsutil di Windows
  maupun macOS (ditampilkan `—`, dan aturan antrean hanya aktif di Linux);
  latensi per volume adalah sinyal utama. Di Windows, counter disk bisa
  ditolak tanpa elevation dan panel menampilkan peringatan.
- **Serial disk**: diambil dari CIM (Windows), sysfs/SMART (Linux), dan
  `SPNVMeDataType`/`SPSerialATADataType` (macOS) lalu ditampilkan di card
  hardware serta laporan insiden; `—` jika vendor/OS tidak mengeksposnya.
- **I/O per proses**: Windows `GetProcessIoCounters` (mencakup cache), Linux
  `/proc/<pid>/io`, macOS `proc_pid_rusage` yang menolak banyak proses sistem
  (tampil `—`). Angka agregat volume tetap akurat.
- **Load average** hanya macOS/Linux. Di Windows alert perlambatan berbasis load
  dilewati; CPU/RAM/disk/jaringan tetap.
- **Port**: TCP `LISTEN` dan UDP bound; proses milik user lain bisa tampil tanpa
  nama.
- **Probe ICMP gateway** bisa diblokir firewall; diagnosis tidak menuduh
  jaringan hanya karena ICMP gagal. Probe TCP `host:port` lebih menentukan.
- **Linux disk I/O**: partisi dan perangkat bertumpuk (`dm-*`, `md*`, `bcache*`,
  `drbd*`, `nbd*`) dilewati saat ada parent fisik agar tidak dihitung ganda.
  Jika hanya perangkat bertumpuk yang ada, perangkat itu dipakai.

## Keamanan

Tidak ada autentikasi. Bawaan hanya `127.0.0.1`; saat memakai `-host 0.0.0.0`
untuk diakses dari PC client, lindungi dengan firewall/VPN atau reverse proxy
berautentikasi. Halaman memakai CSP `default-src 'self'`, tanpa aset eksternal,
dan API menerima JSON maksimal 64 KiB. Data proses/port bisa sensitif.

Pemasangan update mengganti binary di server, jadi tombol Update dan
**pengaturan pembaruan** hanya diterima dari localhost; client LAN mendapat
`403`. Pemasangan otomatis mewajibkan checksum SHA256 dan tidak pernah
berjalan bila Anda tidak mengaktifkannya (bawaan: manual).

## Struktur

```
main.go                     flag/env, SQLite, monitor, server, jendela MyGo
internal/model/             tipe data, pengaturan, validasi
internal/store/             SQLite: settings, samples, alerts, prune
internal/hardware/          health board/CPU/RAM/disk: Windows CIM, Linux, macOS;
                            SMART ATA bawaan (SG_IO / IOCTL), nvme-cli, ssacli (HPE)
internal/diskid/            ID disk stabil (by-id/WWN) dan filter disk virtual (RBD, loop, dm)
internal/update/            GitHub release checker, installer/restart, updater otomatis
internal/monitor/           collector, prober jaringan, mesin alert, diagnosis,
                            loop + SSE hub
internal/server/            HTTP API, SSE, header keamanan, aset embed
internal/server/ui/         index.html, style.css, app.js (tema terang)
tools/icongen/              generator ikon 1024×1024 dengan stdlib
tools/relnotes/             render + validasi catatan rilis dari releases.json
scripts/release.sh          build rilis manual (binary, DMG, checksum, catatan)
scripts/bump-version.sh     menyamakan versi di Makefile, mygo.json, model.go
docs/                       hardware-health.md, updates.md, releasing.md
mygo.json                   konfigurasi paket desktop MyGo
```


## Lisensi

Kode DMetric dilisensikan di bawah **BSD 3-Clause**, lisensi yang sama dengan
Go. Teks lengkap ada di [LICENSE](LICENSE). Copyright © 2026 Dalang Pte Ltd ·
[dalang.io](https://dalang.io).
