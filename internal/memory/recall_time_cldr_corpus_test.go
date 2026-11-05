package memory

import (
	"testing"
	"time"
)

// Ordinary queries with no time meaning, in many languages, must not get a time window
// from the CLDR fallback (nor from the hand-tuned rules).
func TestParseTimeIntentNoFalsePositivesAcrossLanguages(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	corpus := []string{
		// English
		"how do we configure the database pool size", "top 10 errors in the payment service", "chapter 3 summary of the design doc",
		"version 2 of the api client", "who owns the login page", "fix the flaky test in module 12",
		// Korean
		"데이터베이스 연결 풀 설정 방법", "결제 서비스 오류 상위 10개", "로그인 페이지 담당자가 누구야", "버전 2 api 클라이언트 변경점",
		// Japanese
		"データベース接続プールの設定方法", "決済サービスのエラー上位10件", "ログインページの担当者は誰ですか",
		// Chinese
		"如何配置数据库连接池大小", "支付服务前10个错误", "登录页面的负责人是谁", "第3章设计文档摘要",
		// German
		"wie konfigurieren wir die datenbank", "die 10 häufigsten fehler im zahlungsdienst", "kapitel 3 zusammenfassung des entwurfs", "wer ist für die anmeldeseite zuständig",
		// French
		"comment configurer la base de données", "les 10 erreurs principales du service de paiement", "chapitre 3 résumé du document", "qui gère la page de connexion",
		// Spanish
		"cómo configurar la base de datos", "los 10 errores principales del servicio de pagos", "capítulo 3 resumen del documento", "quién es responsable de la página de inicio",
		// Portuguese
		"como configurar o banco de dados", "os 10 principais erros do serviço de pagamento",
		// Italian
		"come configurare il database", "i 10 errori principali del servizio pagamenti",
		// Dutch
		"hoe configureren we de database", "de 10 belangrijkste fouten in de betaaldienst",
		// Russian
		"как настроить пул соединений базы данных", "10 главных ошибок платёжного сервиса", "глава 3 краткое содержание документа", "кто отвечает за страницу входа",
		// Ukrainian
		"як налаштувати базу даних", "10 головних помилок сервісу",
		// Polish
		"jak skonfigurować bazę danych", "10 najczęstszych błędów usługi płatności",
		// Turkish
		"veritabanı nasıl yapılandırılır", "ödeme servisindeki ilk 10 hata",
		// Arabic
		"كيف نضبط قاعدة البيانات", "أهم 10 أخطاء في خدمة الدفع", "من المسؤول عن صفحة تسجيل الدخول",
		// Hindi
		"डेटाबेस को कैसे कॉन्फ़िगर करें", "भुगतान सेवा की 10 मुख्य त्रुटियाँ",
		// Vietnamese
		"cách cấu hình cơ sở dữ liệu", "10 lỗi hàng đầu của dịch vụ thanh toán",
		// Indonesian
		"bagaimana cara mengatur basis data", "10 kesalahan utama layanan pembayaran",
		// Swedish, Finnish, Czech, Greek, Hebrew
		"hur konfigurerar vi databasen", "miten tietokanta määritetään", "jak nastavit databázi", "πώς ρυθμίζουμε τη βάση δεδομένων", "איך מגדירים את מסד הנתונים",
		// Thai
		"วิธีตั้งค่าฐานข้อมูล", "ข้อผิดพลาด 10 อันดับแรก",
		// Mixed technical text with numbers next to words
		"set max_tokens to 300 in the config", "port 8080 conflicts with 5432", "issue 42 mentions the 2 regressions", "release 1 2 3 checklist",
		"the march of progress in our roadmap", "may i ask who wrote this", "sunday school project notes",
		// Words that are a time word in some other CLDR locale but ordinary here
		"3 out of 5 tests fail", "here 2 items left", "masa 3 hari kerja", "de hitte in de serverruimte", "måndag standup anteckningar",
		"the danes won the match", "lørdag planer for teamet", "kamari na wingi wa watu",
	}
	for _, q := range corpus {
		if w, ok := cldrTimeIntent(foldTimeText(q), now); ok {
			t.Errorf("CLDR fallback read %q as time: %q", q, w.Phrase)
		}
		if w, ok := parseTimeIntent(q, now); ok {
			t.Errorf("parseTimeIntent read %q as time: %q", q, w.Phrase)
		}
	}
}
