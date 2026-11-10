# Kesehatan hardware dan SMART

Dokumen ini menjelaskan apa yang dibaca DMetric dari hardware, bagaimana disk
diidentifikasi, dan **mana yang sudah diuji pada perangkat keras nyata**.

## Identitas disk yang stabil

Nama kernel (`sda`, `sdb`, `nvme1n1`) diberikan menurut urutan probe dan bisa
berganti setelah reboot. DMetric tidak memakainya sebagai identitas.

| OS | Identitas | Sumber |
|---|---|---|
| Linux | WWN, lalu EUI, lalu `model_serial`, lalu `scsi-3…` | `/dev/disk/by-id` (udev) |
| Windows | `naa.<hex>` / `eui.<hex>`, atau `model_serial` | `Get-PhysicalDisk` → `UniqueId` |
| macOS | nama dari `system_profiler` | — |

Aturan Linux (`internal/diskid`): WWN berisi nol semua (`wwn-0x0000…`, dipakai
sebagian SSD murah) dibuang; alias LVM/device-mapper/md bukan identitas
perangkat keras; bila dua disk mendapat ID sama, keduanya dibedakan otomatis.
Nama OS (`sda`, `PhysicalDrive1`) hanya tampil di tooltip.

Volume memakai ID disk/partisi yang sama (`wwn-…-part2`) di kolom meta tabel
Volume & RAID.

### Yang bukan disk fisik

Ceph RBD (`rbd*`), loop, RAM disk, zram, optical, dan perangkat bertumpuk
(`dm-*`, `md*`, `bcache*`, `drbd*`, `nbd*`) tidak ditampilkan sebagai disk fisik,
tidak muncul sebagai volume, dan tidak ikut dijumlahkan ke I/O disk. Di
hypervisor dengan puluhan image RBD, ini menghilangkan puluhan baris palsu.

## Sumber health per platform

Urutan di Linux: `smartctl` bila ada, lalu pembaca SMART bawaan untuk SATA,
`nvme-cli` untuk NVMe. Jika tidak ada satu pun yang berhasil, health
ditampilkan **"Tidak diketahui" beserta alasannya**, tidak pernah diam-diam
"Normal".

| Sumber | Platform | Perlu | Isi |
|---|---|---|---|
| Pembaca ATA bawaan (SG_IO) | Linux | root | atribut SMART lengkap + ambang + vonis lulus/gagal, tanpa smartmontools |
| Pembaca ATA bawaan (`IOCTL_ATA_PASS_THROUGH`) | Windows | Administrator | sama; hanya disk SATA yang tampil langsung ke OS |
| `smartctl -j` | Linux | root, smartmontools | ATA, NVMe, SAS; `-d megaraid,N` untuk drive di belakang MegaRAID/PERC |
| `nvme smart-log -o json` | Linux | root, nvme-cli | semua metrik log kesehatan NVMe |
| `Get-StorageReliabilityCounter` | Windows | Administrator | suhu, wear, jam nyala, error |
| `ssacli` | Windows, Linux | HPE Smart Storage Administrator | lihat di bawah |
| `system_profiler` | macOS | — | status SMART ringkas |

Pembaca ATA bawaan mengirim tiga perintah **baca-saja** yang sama dengan
smartctl: `SMART READ DATA`, `SMART READ THRESHOLDS`, dan `SMART RETURN STATUS`.
Halaman data diperiksa checksum-nya; bila rusak (HBA yang tidak mendukung
pass-through) hasilnya ditolak dan UI menampilkan alasan, bukan data palsu.

### Tabel detail SMART

Klik nama disk di kartu **Kesehatan hardware** untuk membuka semua metrik:
ID atribut, nama, nilai ternormalisasi, worst, ambang, nilai mentah, dan status.
NVMe menampilkan data ditulis/dibaca, unsafe shutdown, power cycle, spare,
sensor suhu, dan error integritas.

Penilaian status per atribut:

- **Kritis** bila nilai ternormalisasi ≤ ambang drive sendiri (ambang 0 = tidak
  pernah gagal).
- **Waspada** bila atribut penghitung error yang artinya universal (5, 187,
  196, 197, 198, 199) bernilai mentah > 0.
- Atribut vendor-specific (mis. total program/erase fail, runtime bad block)
  **hanya dinilai dari ambangnya**, karena nilai mentahnya berarti hal berbeda
  di tiap drive; menandainya akan menghasilkan alarm palsu.

## HPE Smart Array (`ssacli`)

Smart Array dalam mode RAID hanya menampilkan logical volume ke OS; drive
fisiknya tidak terlihat oleh SMART maupun `Get-PhysicalDisk`. Bila `ssacli`
terpasang (PATH atau lokasi bawaan installer HPE), DMetric menjalankan
`ssacli ctrl all show config detail` dan menampilkan:

- **Drive fisik**: status, suhu saat ini, jam nyala, sisa umur SSD, model,
  serial, WWID, array tempatnya, serta drive *unassigned*.
- **Logical drive**: level RAID, status, caching, strip size, jumlah drive.
  Dicocokkan dengan disk OS lewat *Unique Identifier* (NAA). Yang tidak cocok
  tampil sebagai baris sendiri supaya status degraded tidak hilang.
- **Controller**: baris tersendiri berisi status controller, **cache**, dan
  **baterai/kapasitor** — penyebab klasik latensi tulis tinggi di RAID 5 bila
  cache dimatikan karena baterai gagal.

Pemetaan status: `OK` → Normal; `Failed`/`Missing` → Kritis;
`Predictive Failure` → Kritis; `Interim Recovery Mode` (redundansi hilang) →
Kritis; `Rebuilding`/`Recovering`/`Ready for Rebuild` → Waspada. Satu drive
gagal atau RAID degraded menjadikan status hardware keseluruhan **Kritis**.

## Status verifikasi

| Bagian | Status |
|---|---|
| Linux: ID stabil, filter RBD, SMART ATA bawaan (SATA), `nvme-cli` | **Teruji di perangkat keras nyata** (4 SSD SATA + 1 NVMe pada Dell PowerEdge R630) |
| Linux: label RAID virtual (PERC) | Teruji; drive fisik di belakang PERC **belum** terbaca |
| Linux: `smartctl -d megaraid,N` | **Belum teruji** pada perangkat keras |
| Windows: `UniqueId`, SMART bawaan, `Get-StorageReliabilityCounter` | Kompilasi dan tes parser saja; **belum dijalankan di Windows nyata** |
| `ssacli` (HPE) | Parser diuji dengan **contoh buatan** yang mengikuti format dokumentasi HPE; **belum dibandingkan dengan output server nyata**. Ejaan kunci (terutama sisa umur SSD) berbeda antar versi |
| macOS | Teruji secara fungsional (tampilan dan disk Apple) |

Pengaman bila ternyata keliru: halaman SMART dicek checksum-nya, vonis selain
`4F/C2` atau `F4/2C` dianggap *tidak diketahui* (bukan *lulus*), format
`ssacli` yang tidak dikenal tidak menghasilkan baris, dan semua kegagalan
membaca ditampilkan sebagai catatan di UI.

## Berkontribusi: output perangkat keras nyata

Pull request yang menambah fixture dari perangkat keras nyata sangat
membantu, terutama:

- output `ssacli ctrl all show config detail` dari berbagai generasi Smart Array,
- keluaran `smartctl -j -a` untuk disk SAS/RAID,
- hasil `Get-PhysicalDisk | Format-List *` dan `Get-StorageReliabilityCounter`.

**Hapus data identitas sebelum membagikan**: nomor seri, WWN/WWID, *Unique
Identifier*, hostname, dan IP. Ganti dengan nilai buatan yang berbentuk sama
(panjang dan huruf/angka heksa yang sama) agar parser tetap teruji.
