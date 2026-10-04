package fetcher

// Pages the owner's library stored as articles before the text rules
// (pagetext.go), as Jina rendered them, each named by its document's ID.
// They are the library's own text, trimmed of whole lines only.

const (
	// Not-found pages the library stored through Jina without a target-status
	// warning, under titles no title rule reads as one.

	// mediumNotFoundBody is 7ad607e2's, titled "Medium": Medium's 404 page,
	// behind its navigation (5 more like it). Trimmed after the first story
	// card.
	mediumNotFoundBody = "[Sitemap](http://blog.palantir.com/sitemap/sitemap.xml)\n\n" +
		"[Open in app](https://play.google.com/store/apps/details?id=com.medium.reader&referrer=utm_source%3DmobileNavBar&source=---not_found_layout_nav-------------------------------------------)\n\n" +
		"Sign up\n\n" +
		"[Sign in](https://medium.com/m/signin?operation=login&redirect=https%3A%2F%2Fblog.palantir.com%2F2007%2F05%2F17%2Fjtable-mouseover-editing&source=login---not_found_layout_nav-----------------------global_nav--------------------)\n\n" +
		"[](https://medium.com/?source=---not_found_layout_nav-------------------------------------------)\n\n" +
		"Get app\n\n" +
		"[Write](https://medium.com/m/signin?operation=register&redirect=https%3A%2F%2Fmedium.com%2Fnew-story&source=---not_found_layout_nav-----------------------new_post_topnav--------------------)\n\n" +
		"[Search](https://medium.com/search?source=---not_found_layout_nav-------------------------------------------)\n\n" +
		"Sign up\n\n" +
		"[Sign in](https://medium.com/m/signin?operation=login&redirect=https%3A%2F%2Fblog.palantir.com%2F2007%2F05%2F17%2Fjtable-mouseover-editing&source=login---not_found_layout_nav-----------------------global_nav--------------------)\n\n" +
		"![Image 1: Unknown user](https://miro.medium.com/v2/resize:fill:64:64/1*dmbNkD5D-u45r44go_cf0g.png)\n\n" +
		"PAGE NOT FOUND\n\n" +
		"## 404\n\n" +
		"## Out of nothing, something.\n\n" +
		"You can find (just about) anything on Medium — apparently even a page that doesn’t exist. Maybe these stories will take you somewhere new?\n\n" +
		"[Home](http://blog.palantir.com/)\n\n" +
		"[](https://medium.com/dualpathway/at-the-edge-of-the-possible-65c4813b7644)\n\n" +
		"#### [The Edge of the Possible](https://medium.com/dualpathway/at-the-edge-of-the-possible-65c4813b7644)\n\n" +
		"[![Image 2: Aleksander Teisseyre](https://miro.medium.com/v2/resize:fill:80:80/1*AJ-ZDdUPFs1vJzvabcfC2w.png)](https://medium.com/@alekteis)\n\n" +
		"[Aleksander Teisseyre](https://medium.com/@alekteis)[in DualPathway](http://blog.palantir.com/dualpathway)\n\n" +
		"Sep 12, 2026\n\n" +
		"·\n\n" +
		"12 min read\n\n" +
		"Member-only"

	// theWeekNotFoundBody is b94ed7b8's, untitled. Trimmed of part of its menu
	// and its tracking pixels.
	theWeekNotFoundBody = "[![Image 2: The Week](https://cdn.mos.cms.futurecdn.net/flexiimages/qsgislpohy1687441729.svg)The Week](https://theweek.com/)\n\n" +
		"[![Image 3: https://cdn.mos.cms.futurecdn.net/flexiimages/jacafc5zvs1692883516.jpg](https://cdn.mos.cms.futurecdn.net/flexiimages/jacafc5zvs1692883516.jpg) SUBSCRIBE & SAVE Less than $3 per week](https://subscribe.theweek.com/servlet/OrdersGateway?cds_mag_code=TWE&cds_page_id=275740&cds_response_key=I4BRBKSW1&itm_medium=referral&itm_source=theweek.com&itm_campaign=wku-all-digital_referral-202401-sub-none-fbk24&itm_content=us-header-block)\n\n" +
		"×Search\n\n" +
		"Sign in\n\n" +
		"*   View Profile\n" +
		"*   Sign out\n\n" +
		"*   [](https://theweek.com/)\n" +
		"*   [The Explainer](https://theweek.com/the-explainer)\n" +
		"*    More \n" +
		"    *   [Politics](https://theweek.com/politics)\n" +
		"    *   [World News](https://theweek.com/tag/world-news)\n\n" +
		"*   [Newsletter sign up Newsletter](https://theweek.com/newsletters)\n\n" +
		"# Sorry!\n\n" +
		"The page you're looking for can't be found.\n\n" +
		"![Image 4: 404 page not found](https://vanilla.futurecdn.net/theweek/404image.jpg)\n\n" +
		"Please try searching our site or [start again on our homepage](https://theweek.com/).\n\n" +
		"Search\n\n" +
		"[](https://theweek.com/)\n" +
		"*   [About Us](https://theweek.com/about-us)\n" +
		"*   [Contact Future's experts](https://futureplc.com/contact/)\n" +
		"*   [Terms and Conditions](https://futureplc.com/terms-conditions/)\n" +
		"*   [Privacy Policy](https://futureplc.com/privacy-policy/)\n" +
		"*   [Cookie Policy](https://futureplc.com/cookies-policy/)\n" +
		"*   [Advertise With Us](https://go.future-advertising.com/The-Week-Media-Kit.html)\n" +
		"*   [FAQ](https://theweek.com/faq)\n" +
		"*   [Do not sell or share my personal information](http://theweek.com/privacy-portal)"

	// bespokeNotFoundBody is abd7cbb3's, titled "Bespoke Interactive". Trimmed
	// of its footer.
	bespokeNotFoundBody = "![Image 1](https://www.bespokepremium.com/interactive/img/arrow-left.png)\n\n" +
		"[Home](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)\n\n" +
		"Research\n\n" +
		"[Starred](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Bespoke Report](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Morning Lineup](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)\n\n" +
		"Tools\n\n" +
		"[Bespoke Charts](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Bespoke Charts Scanner](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Bespoke Earnings Tool](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Bespoke Seasonality Tool](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Bespoke Trend Analyzer](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)\n\n" +
		"My Portfolios\n\n" +
		"[![Image 2](https://www.bespokepremium.com/interactive/img/logo_white_icon.png)![Image 3](https://www.bespokepremium.com/interactive/img/logo_white_text.png)](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[🤔 No results found. Try typing _home_ or _$MSFT_](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Search](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Research](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Tools](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)[Portfolios](https://www.bespokepremium.com/think-big-blog/best-performing-stocks-around-the-world-since-trumps-election/)Log In\n\n" +
		"Account\n\n" +
		"[Account](https://www.bespokepremium.com/interactive/account/)[Settings](https://www.bespokepremium.com/interactive/account/)[Sign Out](https://www.bespokepremium.com/logout-2/)\n\n" +
		"# 404\n\n" +
		"# Sorry, but we can't find the page you're looking for.\n\n" +
		"  \n" +
		"Go Home\n\n" +
		"©2026 Bespoke Investment Group"

	// javaLobbyNotFoundBody is fa2c61a1's, titled "หน้าไม่พบ | JavaLobby" (Thai
	// for "Page not found"; 3219e2f1 is the same), whole.
	javaLobbyNotFoundBody = "## 404 - หน้าไม่พบ\n\n" +
		"ขออภัย, หน้าที่คุณกำลังค้นหาไม่มีอยู่หรืออาจได้รับการย้าย. กรุณาตรวจสอบ URL และลองอีกครั้ง.\n\n" +
		"ในระหว่างนี้, ทำไมไม่ลองสำรวจสิ่งใหม่? เราเพิ่งเปิดตัวเกมใหม่ที่ชื่อ [**PG**](https://www.javalobby.org/)Slot ซึ่งเป็นเกม Java สุดล้ำจาก **JavaLobby.org**.\n\n" +
		"สนใจเกมคาสิโน? ลองเล่น **[PG Slot](https://www.javalobby.org/)**, เกมที่ให้ความสนุกไม่รู้จบ!\n\n" +
		"pgslot: กลับไปที่หน้าแรกของเราได้ที่ [JAVALOBBY](https://www.javalobby.org/) เพื่อดูข้อมูลเพิ่มเติม.\n\n" +
		"It looks like the page you’re searching for doesn’t exist. But don’t worry, exciting things are happening at JavaLobby.org! Did you know we're now diving into the world of gaming in Thailand?\n\n" +
		"Our latest creation, PG Slot, is a groundbreaking Java game set to revolutionize the way you play.\n\n" +
		"Whether you’re here for PGSlot or curious about our latest innovations, stick around as we bring new experiences to life.\n\n" +
		"Head back to the homepage and explore our updates!"

	// linkedInNotFoundBody is 3ef4dab9's, titled "Top Content on LinkedIn"
	// (2b977a4b is the same): its notice is its first long line. Trimmed after
	// its first heading.
	linkedInNotFoundBody = "[Skip to main content](https://www.linkedin.com/pulse/wework-apple-real-estate-dror-poleg/#main-content)[LinkedIn](https://www.linkedin.com/?trk=keyword-landing-homepage_nav-header-logo)\n" +
		"*   [Top Content](https://www.linkedin.com/top-content?trk=keyword-landing-homepage_guest_nav_menu_topContent)\n" +
		"*   [People](https://www.linkedin.com/pub/dir/+/+?trk=keyword-landing-homepage_guest_nav_menu_people)\n" +
		"*   [Learning](https://www.linkedin.com/learning/search?trk=keyword-landing-homepage_guest_nav_menu_learning)\n" +
		"*   [Jobs](https://www.linkedin.com/jobs/search?trk=keyword-landing-homepage_guest_nav_menu_jobs)\n" +
		"*   [Games](https://www.linkedin.com/games?trk=keyword-landing-homepage_guest_nav_menu_games)\n\n" +
		"[Join now](https://www.linkedin.com/signup/cold-join?session_redirect=%2Ftop-content%2F%3Ftrk%3Darticle_not_found&trk=keyword-landing-homepage_nav-header-join)[Sign in](https://www.linkedin.com/uas/login?session_redirect=%2Ftop-content%2F%3Ftrk%3Darticle_not_found&fromSignIn=true&trk=keyword-landing-homepage_nav-header-signin)[![Image 1](https://www.linkedin.com/pulse/wework-apple-real-estate-dror-poleg/)](https://www.linkedin.com/uas/login?session_redirect=%2Ftop-content%2F%3Ftrk%3Darticle_not_found&fromSignIn=true&trk=keyword-landing-homepage_nav-header-signin)\n\n" +
		"We can’t find the page you’re looking for.The page you’re looking for may have been moved, or may no longer exist. Try going back to the previous page or check out top LinkedIn content from expert professionals.\n\n" +
		"# What topics do you want to explore?"

	// quartzNotFoundBody is 3ef6449a's, titled "Quartz": Next.js's 404 under
	// the site's header, which the answer repeats. Trimmed to the second copy.
	quartzNotFoundBody = "[Subscribe](https://work.qz.com/newsletter)\n\n" +
		"Edition\n\n" +
		"English\n\n" +
		"[Business News](https://work.qz.com/business-news)\n\n" +
		"[A.I.](https://work.qz.com/tech-innovation/ai)\n\n" +
		"[Technology](https://work.qz.com/tech-innovation)\n\n" +
		"[Money & Markets](https://work.qz.com/money-markets)\n\n" +
		"[Leadership](https://work.qz.com/leadership)\n\n" +
		"[Lifestyle](https://work.qz.com/lifestyle)\n\n" +
		"[Video](https://work.qz.com/video)\n\n" +
		"[Latest](https://work.qz.com/latest)\n\n" +
		"#### Get Quartz in your inbox\n\n" +
		"Free daily briefing on global business news.\n\n" +
		"Sign me up\n\n" +
		"Switch to Dark Mode\n\n" +
		"Menu\n\n" +
		"Business News\n\n" +
		"[Airlines](https://work.qz.com/business-news/airlines)[Automobiles](https://work.qz.com/business-news/autos)[Food](https://work.qz.com/business-news/food)[Pharmaceuticals](https://work.qz.com/business-news/pharma)[Politics & Government](https://work.qz.com/business-news/politics-government)[Retail & Ecommerce](https://work.qz.com/business-news/retail)[Space & Aerospace](https://work.qz.com/business-news/air-and-space)[Earnings](https://work.qz.com/money-markets/earnings-snapshots)\n\n" +
		"Menu\n\n" +
		"Technology\n\n" +
		"[A.I.](https://work.qz.com/tech-innovation/ai)[Computing](https://work.qz.com/tech-innovation/cloud-computing)[Consumer Tech](https://work.qz.com/tech-innovation/consumer-tech)[Space & Aerospace](https://work.qz.com/tech-innovation/space)[Earnings](https://work.qz.com/money-markets/earnings-snapshots)\n\n" +
		"Menu\n\n" +
		"Money & Markets\n\n" +
		"[Economic Indicators](https://work.qz.com/money-markets/economic-indicators)[Markets](https://work.qz.com/money-markets/markets)[Personal Finance](https://work.qz.com/money-markets/personal-finance)[Earnings](https://work.qz.com/money-markets/earnings-snapshots)\n\n" +
		"Menu\n\n" +
		"Lifestyle\n\n" +
		"[Cars & Bikes](https://work.qz.com/lifestyle/cars)[Collecting](https://work.qz.com/lifestyle/collecting)[Entertainment](https://work.qz.com/lifestyle/entertainment)[Food & Fine Dining](https://work.qz.com/lifestyle/food-fine-dining)[Health and Fitness](https://work.qz.com/lifestyle/health-fitness)[Real Estate](https://work.qz.com/lifestyle/real-estate)[Travel](https://work.qz.com/lifestyle/travel)\n\n" +
		"# 404\n\n" +
		"This page could not be found.\n\n" +
		"[Go home](https://work.qz.com/)\n\n" +
		"[![Image 6: Quartz](https://static.qz.com/quartz-v1.130.0/_next/static/media/quartz-logo.1poxn91047rb6.svg?dpl=089cs7uw65wq)](https://work.qz.com/)\n" +
		"Global business news for a smarter world"

	// advisorPerspectivesNotFoundBody is d932fad2's, titled "Advisor
	// Perspectives": the notice sits 19.3 KB into the answer, behind its menus.
	// Trimmed to a few of their items.
	advisorPerspectivesNotFoundBody = "## [Helping advisors enable clients to achieve their financial goals](http://advisorperspectives.com/)Toggle navigation\n\n" +
		"[Advanced Search](http://advisorperspectives.com/search)\n\n" +
		"*   [Newsletter](http://advisorperspectives.com/newsletter/subscribe)[Log In](http://advisorperspectives.com/subscriber/login)\n\n" +
		"Toggle navigation\n" +
		"*   [Insights & Education](http://advisorperspectives.com/newsletters14/10-Buffett3.php#)\n" +
		"    *   [Articles](http://advisorperspectives.com/articles)\n" +
		"    *   [AP Charts](http://advisorperspectives.com/dshort)\n\n" +
		"*   [Podcasts & Videos](http://advisorperspectives.com/newsletters14/10-Buffett3.php#)\n" +
		"    *   [Gaining Perspectives Podcast](http://advisorperspectives.com/podcasts/gaining-perspective)\n" +
		"    *   [ETF of the Week Podcast](http://advisorperspectives.com/podcasts/vettafi-etf-of-the-week)\n" +
		"    *   [ETF Prime Podcast](http://advisorperspectives.com/podcasts/etf-prime-with-nate-geraci)\n" +
		"    *   [All Podcasts](http://advisorperspectives.com/podcasts)\n" +
		"    *   [Videos](http://advisorperspectives.com/videos)\n\n" +
		"[Subscribe to newsletter](http://advisorperspectives.com/newsletter/subscribe)[Log In](http://advisorperspectives.com/subscriber/login)\n\n" +
		"[Advanced Search](http://advisorperspectives.com/search)\n\n" +
		"Close ×\n\n" +
		"## 404 Error: Not Found\n\n" +
		"We're sorry but we seem to be experiencing some technical difficulties. Please contact [our support staff](mailto:support@advisorperspectives.com?subject=Page%20Not%20Found:%20%2Fnewsletters14%2F10-Buffett3.php) for assistance.\n\n" +
		"#### Sponsored Content"

	// Parked, for-sale and expired domains the library stored through Jina.

	// goDaddyForSaleBody is 9d57ab40's, titled "app.io is for sale — Get a
	// price in 24 hours": GoDaddy's sale page, its domain's name dropped (11
	// more like it), whole.
	goDaddyForSaleBody = "###### The domain name\n\n" +
		"###### is for sale!\n\n" +
		"Premium\n\n" +
		"Verified Domain\n\n" +
		"##### Get a price in less than 24 hours\n\n" +
		"* * *\n\n" +
		"First Name\n\n" +
		"Last Name\n\n" +
		"Email\n\n" +
		"Phone\n\n" +
		"United States\n\n" +
		"By submitting, you agree to our .\n\n" +
		"Need a price instantly? Contact us now.\n\n" +
		"[1-855-646-1390](https://forsale.godaddy.com/forsale/true)(Toll Free in the U.S. and Canada)\n\n" +
		"[+1 781-373-6808](https://forsale.godaddy.com/forsale/true)(International number)\n\n" +
		"![Image 1: Safe & secure transactions](https://forsale.godaddy.com/forsale/imgs/dan-custom/secure-transactions.svg)\n\n" +
		"Safe & secure transactions\n\n" +
		"![Image 2: Fast & easy transfers](https://forsale.godaddy.com/forsale/imgs/dan-custom/fast-and-easy-transfers.svg)\n\n" +
		"Fast & easy transfers\n\n" +
		"![Image 3: Hassle free payments](https://forsale.godaddy.com/forsale/imgs/dan-custom/hassle-free-payments.svg)\n\n" +
		"Hassle free payments\n\n" +
		"* * *\n\n" +
		"* * *\n\n" +
		"###### The simple, and safe way to buy domain names\n\n" +
		"No matter what kind of domain you want to buy or lease, we make the transfer simple and safe."

	// namecheapExpiredBody is 397f7090's, titled "icefilms.info is registered
	// at Namecheap", whole.
	namecheapExpiredBody = "## Domain registration has expired.\n\n" +
		"### Renewal instructions\n\n" +
		"If you own **icefilms.info**, you may still be able to renew it through your **Namecheap** account. To renew: sign in, open **Domain List**, select **icefilms.info**, and choose **Renew**.\n\n" +
		"[Sign in](https://www.namecheap.com/myaccount/login/?utm_source=parkingpage&utm_medium=referral&utm_campaign=nc_market) · [Renewal help](https://www.namecheap.com/support/knowledgebase/article.aspx/239/2201/how-can-i-renew-my-domain/?utm_source=parkingpage&utm_medium=referral&utm_campaign=nc_market) · [Renewal and redemption fees](https://www.namecheap.com/support/knowledgebase/article.aspx/242/2207/what-is-the-domain-redemption-grace-period/?utm_source=parkingpage&utm_medium=referral&utm_campaign=nc_market)\n\n" +
		"[Renew this domain](https://ap.www.namecheap.com/?utm_source=parkingpage&utm_medium=referral&utm_campaign=nc_market)"

	// namecheapRegisteredBody is b767c66d's, untitled, whole.
	namecheapRegisteredBody = "[![Image 1: Namecheap Logo](http://headlime.io/logo.svg)](https://www.namecheap.com/?utm_source=parkingpage&utm_medium=referral&utm_campaign=nc_market)\n\n" +
		"has been recently registered with namecheap.com\n\n" +
		"## Want a domain name like this?\n\n" +
		"Discover domains on auction now\n\n" +
		"[See all auctions](https://www.namecheap.com/market/?utm_source=parkingpage&utm_medium=referral&utm_campaign=nc_market)\n\n" +
		"## Download the Namecheap Auctions app\n\n" +
		"Take auctions with you wherever you go\n\n" +
		"*   [![Image 2: Download on the App Store](http://headlime.io/appstore.svg)](https://apps.apple.com/us/app/namecheap-auctions/id6743634772?itscg=30200&itsct=apps_box_badge&mttnsubad=6743634772)\n" +
		"*   [![Image 3: Get it on Google Play](http://headlime.io/googleplay.svg)](https://play.google.com/store/apps/details?id=marketplace.com.namecheap)"

	// hoverParkedBody is 0fce584d's, untitled: Hover's parked page, whole.
	hoverParkedBody = "## aptfolk.com\n\n" +
		"## is a totally awesome idea still being worked on.\n\n" +
		"Check back later.\n\n" +
		"*   [Home](https://www.hover.com/?source=parked)\n" +
		"*   [Transfer](https://www.hover.com/transfer_in?source=parked)\n" +
		"*   [Renew](https://www.hover.com/renew?source=parked)\n" +
		"*   [Domain Pricing](https://www.hover.com/domain_pricing?source=parked)\n" +
		"*   [Email](https://www.hover.com/email?source=parked)\n" +
		"*   [About Us](https://www.hover.com/about?source=parked)\n" +
		"*   [Help](https://help.hover.com/home?source=parked)\n" +
		"*   [Your Account](https://www.hover.com/tools?source=parked)\n\n" +
		"*   [](https://www.facebook.com/hover)\n" +
		"*   [](https://www.instagram.com/hover_domains)"

	// easyDNSParkedBody is fcb281ad's, titled "Parked Domain | easyDNS", whole.
	easyDNSParkedBody = "[![Image 1: easyDNS](http://www.profitguide.com/assets/easydns-logo-CRr2z_ip.png)](https://easydns.com/)\n\n" +
		"profitguide.com\n\n" +
		"is yet another domain managed by easyDNS\n\n" +
		"[This Domain is Parked: Learn more](http://www.profitguide.com/learn-more \"This Domain is Parked: Learn more\")\n\n" +
		"![Image 2](http://www.profitguide.com/easybolt.svg)\n\n" +
		"Register, transfer or host your domains and websites with easyDNS\n\n" +
		"[Learn More](https://easydns.com/)\n\n" +
		"### AxisOfEasy Podcast\n\n" +
		"[Video 3](https://www.youtube.com/watch?v=M47q2KiAz5A)\n\n" +
		"[Subscribe](https://axisofeasy.com/join)\n\n" +
		"### Quick and easy Reverse Tunnels\n\n" +
		"![Image 3](https://pbs.twimg.com/media/HJIFgKyXEAA9JD0?format=jpg&name=medium)\n\n" +
		"[Free localhost tunnels in under 60 seconds](https://tunnel.to/)\n\n" +
		"### You deploy openclaw, we handle the hosting\n\n" +
		"![Image 4](https://ichsbkrrcubpuogwmdpw.supabase.co/storage/v1/object/public/logos/editor/88dce41c-6adb-4b31-ab87-7e13b2ec4d12.jpg)\n\n" +
		"[First Month Free (Promo code: WELCOME26)](https://get.easynode.ai/easyclaw)\n\n" +
		"### It's Always The DNS...\n\n" +
		"![Image 5](https://ichsbkrrcubpuogwmdpw.supabase.co/storage/v1/object/public/logos/editor/516b82b3-9c44-44bd-8e11-8fb71fdeb868.jpeg)\n\n" +
		"[But it doesn't have to be...](https://easydns.com/dns)"

	// searchAdsParkingBody is 5b84d76b's, untitled: a search-ads parking page,
	// 9,150 bytes around 199 of words. Trimmed to its first ad link.
	searchAdsParkingBody = "[](http://www.wetwalls.ca/)\n\n" +
		"Related Search Topics\n\n" +
		"*    [Wet Walls For Bathrooms](https://search-domainparking.com/lander?serveURL=https%3A%2F%2Fwetwalls.ca&system=PW&domain=wetwalls.ca&feedid=2&type=PW_ROLLER_ADS_BACKFILL&qs=qsx-5c5d218b6b9c7f2bc3695cdafe921ef37a097cc1d18abe0afdc69b71390afd1211bc9bf184263c0ff5672593a13c6256eb0bf6c53254be01538746b0bfa267181cc603535545097f1747f4d03c2b48695196372986975917f77daeda06561f8f4a1594a4669f19be3bf304fdb183f2484a815480ce6e4372d53ddaebcf0bc3c20b1840cc2fbd4d36beea4d5a1cb232f0b9d8e48cc13a61ac290e6a81b03efe83299f19b02b58ca01d47911160314a9a27ff5b1231896a08961157fa51a3585ab87716b6d8091e1a6a05ad55a6ae236e527fdd7da04f41bb6a82863f9410bc81b85f6e843d79f2439da94dfabab95ac3cc50f27bf394e37750d6776e76e9237cbb1910cff9f352c6da0bf5d2eeebcd887e0978a18d30b5f8e0f7c69ae25a963d637d450d26ea5cedf2700d285b007326c9776a1dd0f4ef229ff3549846108825d59f310d1a55ae4b404026fc39b155c98d3350d546fb8ec311a23aed4399a97cdc6edba4dcad109aac4d1e4182ea8c34e26cd627289021bd9ebd499b2d38ede4b47c3f692190a2c17e7c8a10e538217ea99a08d5832d49e673e928af3d830960f65fdabdb0212749ce6a83fa8aa4611b4e1f7615c1d5c118c86db91f6cbb932e9f12418e6cc228a0e7b426c120defe38982a2cdd448244467e011a97cbcf01008d3f9efcbfc506809ac38dc03bf769cf4c2f713c83220b306f22d1512af870a11cb54c76f8295ecdb5f16408ad3f76d38fac92b671dbad7c47748d93f06c37b916480fde9f273bd6a58f6698fba65b7226b0dbb4aff0d7aa3493a56bfcc7d901b2bb644044032882cfc0cd49d80d796c771d4e8ea775f0ad968951d03b3afb0450553a59b1f3deb16dde6b7fe73f5bc69087b988c081f7497add95607a4fe10a06eccebf9fa420035.060b1d7ad440e116a10681cfdb67233d) \n\n" +
		"Copyright © All rights reserved.[Privacy Policy](http://www.wetwalls.ca/#!)"

	// flappyRoyaleParkedBody is 9225309d's, titled "HeadlineLogic News Portal":
	// a parking page with a sign-in box at its top. Trimmed of its ad
	// headlines.
	flappyRoyaleParkedBody = "[![Image 1: HeadlineLogic Banner](https://flappyroyale.io/templates/newstemplate/images/logo_small.png)](https://flappyroyale.io/)\n\n" +
		"*   [](javascript:void(0);)\n" +
		"*   [News](https://flappyroyale.io/ \"News\")\n\n" +
		"*   [Entertainment](https://flappyroyale.io/Entertainment \"Entertainment\")\n\n" +
		"*   [Weather](https://flappyroyale.io/Weather \"Weather\")\n\n" +
		"*   [Sports](https://flappyroyale.io/Sports \"Sports\")\n\n" +
		"*   [Finance](https://flappyroyale.io/Finance \"Finance\")\n\n" +
		"*   [Lifestyle](https://flappyroyale.io/Lifestyle \"Lifestyle\")\n\n" +
		"*   [Health](https://flappyroyale.io/Health \"Health\")\n\n" +
		"*   [Dining](https://flappyroyale.io/Dining \"Dining\")\n\n" +
		"*   [Travel](https://flappyroyale.io/Travel \"Travel\")\n\n" +
		"*   [Autos](https://flappyroyale.io/Autos \"Autos\")\n\n" +
		"*   [Video](https://flappyroyale.io/Video \"Video\")\n\n" +
		"[Sign-up](javascript:void(0))\n\n" +
		"[![Image 2: User](https://flappyroyale.io/templates/newstemplate/images/user-light-1.svg)](https://flappyroyale.io/#)\n\n" +
		"LOGIN\n\n" +
		"User Name * \n\n" +
		"Password * \n\n" +
		"[Sign-up](javascript:void(0))[Forgot Password?](https://flappyroyale.io/#)\n\n" +
		"Login\n\n" +
		"User Name * \n\n" +
		"Submit\n\n" +
		"[← Back to Login](https://flappyroyale.io/#)\n\n" +
		"This domain name may be for sale. [Click here to find out or call +1-866-284-4125](https://www.godaddy.com/forsale/flappyroyale.io?utm_source=TDFS_BINNS2&utm_medium=parkedpages&utm_campaign=x_corp_tdfs-binns2_base&traffic_type=TDFS_BINNS2&traffic_id=binns2&)\n\n" +
		"*   [Home](https://flappyroyale.io/?d=flappyroyale.io&pcid=56&a=false&uuid=a8e9e07bf2205ab91dc4401cdf166df2&brand=flappyroyale.io&pgid=154&tbid=322)"

	// Bot checks the library stored through Jina, on pages of 2 KiB or less.

	// googleSorryBody is 16cc372f's, titled with the search URL: Google's
	// unusual-traffic page, whole.
	googleSorryBody = "**About this page**\n\n" +
		"Our systems have detected unusual traffic from your computer network. This page checks to see if it's really you sending the requests, and not a robot. [Why did this happen?](https://www.google.ca/search?aqs=chrome..69i57j0.5420j0&ie=UTF-8&oq=what+data+types+shoul+di+use+for+money+in+java&q=what+data+types+shoul+di+use+for+money+in+java&sourceid=chrome#)\n\n" +
		"IP address: 2600:1900:0:2d12::1101  \n" +
		"Time: 2026-09-28T07:05:20Z  \n" +
		"URL: https://www.google.ca/search?aqs=chrome..69i57j0.5420j0&ie=UTF-8&oq=what+data+types+shoul+di+use+for+money+in+java&q=what+data+types+shoul+di+use+for+money+in+java&sourceid=chrome&sei=rxG6apKPHf6SruEPtKDDwAw"

	// googleSorryTitle is 16cc372f's: the search URL, as Google's page titles it.
	googleSorryTitle = "https://www.google.ca/search?aqs=chrome..69i57j0.5420j0&ie=UTF-8&oq=what+data+types+shoul+di+use+for+money+in+java&q=what+data+types+shoul+di+use+for+money+in+java&sourceid=chrome&sei=rxG6apKPHf6SruEPtKDDwAw"

	// googleSorryLongBody is e7cc72ef's, the library's longest Google page
	// (1,580 bytes): it quotes the search URL twice. Whole.
	googleSorryLongBody = "**About this page**\n\n" +
		"Our systems have detected unusual traffic from your computer network. This page checks to see if it's really you sending the requests, and not a robot. [Why did this happen?](https://www.google.com/search?ei=43pAX8bxKNK0ggex446ADQ&gs_lcp=CgZwc3ktYWIQAzIFCCEQoAE6BAgAEEc6BQgAEJECOgsILhCxAxDHARCjAjoFCAAQsQM6CggAELEDEIMBEEM6AggAOggILhCxAxCDAToICAAQsQMQgwE6BAgAEEM6AgguOgQIABAKOgoILhDHARCjAhAKOgQILhAKOgoILhDHARCvARAKOgUILhCxAzoICC4QxwEQowI6BAguEEM6BwgAELEDEEM6BwguELEDEEM6DgguELEDEIMBEMcBEK8BOggILhDHARCvAToECAAQDToGCAAQDRAeOgQIIRAVOggIIRAWEB0QHjoGCAAQFhAeOgcIIRAKEKABUMTSAljW7ANggO4DaAxwAXgAgAHcAYgB2yqSAQcyOS4yNC4xmAEAoAEBqgEHZ3dzLXdpesABAQ&oq=mobile+view+is+zoomed+in+on+angular+app&q=mobile+view+is+zoomed+in+on+angular+app&rlz=1C5CHFA_enCA901CA901&sclient=psy-ab&uact=5&ved=0ahUKEwiG75GZ2q3rAhVSmuAKHbGxA9AQ4dUDCAw#)\n\n" +
		"IP address: 2600:1900:0:2d04::1301  \n" +
		"Time: 2026-09-28T07:17:13Z  \n" +
		"URL: https://www.google.com/search?ei=43pAX8bxKNK0ggex446ADQ&gs_lcp=CgZwc3ktYWIQAzIFCCEQoAE6BAgAEEc6BQgAEJECOgsILhCxAxDHARCjAjoFCAAQsQM6CggAELEDEIMBEEM6AggAOggILhCxAxCDAToICAAQsQMQgwE6BAgAEEM6AgguOgQIABAKOgoILhDHARCjAhAKOgQILhAKOgoILhDHARCvARAKOgUILhCxAzoICC4QxwEQowI6BAguEEM6BwgAELEDEEM6BwguELEDEEM6DgguELEDEIMBEMcBEK8BOggILhDHARCvAToECAAQDToGCAAQDRAeOgQIIRAVOggIIRAWEB0QHjoGCAAQFhAeOgcIIRAKEKABUMTSAljW7ANggO4DaAxwAXgAgAHcAYgB2yqSAQcyOS4yNC4xmAEAoAEBqgEHZ3dzLXdpesABAQ&oq=mobile+view+is+zoomed+in+on+angular+app&q=mobile+view+is+zoomed+in+on+angular+app&rlz=1C5CHFA_enCA901CA901&sclient=psy-ab&uact=5&ved=0ahUKEwiG75GZ2q3rAhVSmuAKHbGxA9AQ4dUDCAw&sei=eRS6at4w07yu4Q-Rutsx"

	// fastlyChallengeBody is 134358fc's, titled "Client Challenge": Fastly's
	// bot check, whole.
	fastlyChallengeBody = "A required part of this site couldn’t load. This may be due to a browser extension, network issues, or browser settings. Please check your connection, disable any ad blockers, or try using a different browser.\n\n" +
		"![Image 1: Fastly Logo](https://www.perlmonks.org/_fs-ch-1T1wmsGaOgGaSxcX/assets/fastlyLogoError.svg)\n\n" +
		"![Image 2](https://www.perlmonks.org/_fs-ch-1T1wmsGaOgGaSxcX/assets/errorIcon.svg)Oops, something went wrong.\n\n" +
		"Please check your connection, disable any ad blockers, or try using a different browser."

	// Sign-in walls the library stored through Jina: thin pages whose link
	// URLs put them over minArticleBytes.

	// instagramSignInBody is bd2f791a's, titled "Instagram" (6 more like it),
	// whole.
	instagramSignInBody = "![Image 1](blob:http://localhost/a2c4acc86ce977bdb7718eba199dd413)![Image 2](blob:http://localhost/a7d152ce433b443153f919ae47dd7fc4)\n\n" +
		"See everyday moments from your close friends.\n\n" +
		"![Image 3](https://static.cdninstagram.com/rsrc.php/yN/r/-erGonz07kB.webp)\n\n" +
		"Log into Instagram\n\n" +
		"Mobile number, username or email \n\n" +
		"Password \n\n" +
		"- [x] \n\n" +
		"Log in\n\n" +
		" \n\n" +
		"[Forgot password?](https://www.instagram.com/accounts/password/reset/?next=https%3A%2F%2Fwww.instagram.com%2Fchivexp%2F)\n\n" +
		"Log in with Facebook\n\n" +
		"[Create new account](https://instagram.com/accounts/emailsignup/?next=https%3A%2F%2Fwww.instagram.com%2Fchivexp%2F)\n\n" +
		"* * *\n\n" +
		"[Meta](https://about.meta.com/)\n\n" +
		"[About](https://about.instagram.com/)\n\n" +
		"[Blog](https://about.instagram.com/blog/)\n\n" +
		"[Jobs](https://about.instagram.com/about-us/careers)\n\n" +
		"[Help](https://help.instagram.com/)\n\n" +
		"[API](https://developers.facebook.com/docs/instagram)\n\n" +
		"[Privacy](https://instagram.com/legal/privacy/?next=https%3A%2F%2Fwww.instagram.com%2Fchivexp%2F)\n\n" +
		"[Terms](https://instagram.com/legal/terms/?next=https%3A%2F%2Fwww.instagram.com%2Fchivexp%2F)\n\n" +
		"[Locations](https://instagram.com/explore/locations/?next=https%3A%2F%2Fwww.instagram.com%2Fchivexp%2F)\n\n" +
		"[Popular](https://instagram.com/popular/?next=https%3A%2F%2Fwww.instagram.com%2Fchivexp%2F)\n\n" +
		"[Instagram Lite](https://instagram.com/web/lite/?next=https%3A%2F%2Fwww.instagram.com%2Fchivexp%2F)\n\n" +
		"[Meta AI](https://www.meta.ai/?utm_source=foa_web_footer)\n\n" +
		"[Muse](https://muse.ai/)\n\n" +
		"[Threads](https://www.threads.com/)\n\n" +
		"[Contact Uploading & Non-Users](https://www.facebook.com/help/instagram/261704639352628)\n\n" +
		"[Meta Verified](https://instagram.com/accounts/meta_verified/?entrypoint=web_footer&next=https%3A%2F%2Fwww.instagram.com%2Fchivexp%2F)\n\n" +
		"English\n\n" +
		"© 2026 Instagram from Meta"

	// facebookSignInBody is 748c75b7's, titled "Facebook" (0866b543 is the
	// same). Trimmed of its footer.
	facebookSignInBody = "Explore the things you love.\n\n" +
		"Log into Facebook\n\n" +
		"Email or mobile number \n\n" +
		"Password \n\n" +
		"- [x] \n\n" +
		"Log in\n\n" +
		" \n\n" +
		"[Forgot password?](https://www.facebook.com/recover/initiate/?privacy_mutation_token=eyJ0eXBlIjo1LCJjcmVhdGlvbl90aW1lIjoxNzkwNTc4OTQ0fQ%3D%3D&ars=facebook_login&next=https%3A%2F%2Fwww.facebook.com%2Fnotes%2Ffacebook-engineering%2Fmeet-a-facebook-engineer-andrew-boz-bosworth%2F10150468287928920)\n\n" +
		"[Create new account](https://www.facebook.com/reg/?entry_point=login&next=https%3A%2F%2Fwww.facebook.com%2Fnotes%2Ffacebook-engineering%2Fmeet-a-facebook-engineer-andrew-boz-bosworth%2F10150468287928920)\n\n" +
		"* * *\n\n" +
		"* * *\n\n" +
		"English (US)\n\n" +
		"[Español](https://www.facebook.com/notes/facebook-engineering/meet-a-facebook-engineer-andrew-boz-bosworth/10150468287928920#)\n\n" +
		"[Français (France)](https://www.facebook.com/notes/facebook-engineering/meet-a-facebook-engineer-andrew-boz-bosworth/10150468287928920#)\n\n" +
		"[中文(简体)](https://www.facebook.com/notes/facebook-engineering/meet-a-facebook-engineer-andrew-boz-bosworth/10150468287928920#)"

	// linkedInSignInBody is 9dffe504's, titled "LinkedIn Login, Sign in |
	// LinkedIn" (e1e79ffd is the same). Trimmed of the form's second copy and
	// the footer.
	linkedInSignInBody = "## 0 notifications\n\n" +
		"[](https://www.linkedin.com/)\n\n" +
		"[](https://www.linkedin.com/)\n\n" +
		"# Sign in\n\n" +
		"New to LinkedIn?\n\n" +
		"[Join now](https://www.linkedin.com/signup/cold-join/?fromLogin=true)\n\n" +
		"Continue with Google Continue with Google\n\n" +
		"Sign in with Apple\n\n" +
		"By continuing, you agree to LinkedIn’s[**User Agreement**](https://www.linkedin.com/legal/user-agreement/),[**Privacy Policy**](https://www.linkedin.com/legal/privacy-policy/), and[**Cookie Policy**](https://www.linkedin.com/legal/cookie-policy/).\n\n" +
		"* * *\n\n" +
		"or\n\n" +
		"* * *\n\n" +
		"Email or phone\n\n" +
		" \n\n" +
		"Password\n\n" +
		" \n\n" +
		"[Forgot password?](https://www.linkedin.com/passwordReset/?session_redirect=https%3A%2F%2Fwww.linkedin.com%2Fprofile%2Fview%3FauthToken%3DebNN%26authType%3Dname%26id%3D3544669)\n\n" +
		"- [x] \n\n" +
		"Keep me signed in\n\n" +
		"Sign in"

	// An article the library stores whose opening holds a count on a line
	// of its own, as a status code alone would be.

	// stackOverflowQuestionBody is 85a60cef's: its score, 444, on a line of
	// its own in its opening, as 24 of the library's Stack Exchange
	// questions give theirs. Trimmed after the question.
	stackOverflowQuestionBody = "This question shows research effort; it is useful and clear\n\n" +
		"444\n\n" +
		"This question does not show any research effort; it is unclear or not useful\n\n" +
		"Save this question.\n\n" +
		"[](https://stackoverflow.com/posts/17074365/timeline)\n\n" +
		"Show activity on this post.\n\n" +
		"I recently downloaded [Xcode](http://en.wikipedia.org/wiki/Xcode) 5 DP to test my apps in iOS 7. The first thing I noticed and confirmed is that my view's bounds is not always resized to account for the status bar and navigation bar.\n\n" +
		"In `viewDidLayoutSubviews`, I print the view's bounds:\n\n" +
		"> {{0, 0}, {320, 568}}\n\n" +
		"This results in my content appearing below the navigation bar and status bar.\n\n" +
		"I know I could account for the height myself by getting the main screen's height, subtracting the status bar's height and navigation bar's height, but that seems like unnecessary extra work.\n\n" +
		"How can I fix this issue?\n\n" +
		"**Update:**\n\n" +
		"I've found a solution for this specific problem. Set the navigation bar's translucent property to NO:\n\n" +
		"```\nself.navigationController.navigationBar.translucent = NO;\n```\n\n" +
		"This will fix the view from being framed underneath the navigation bar and status bar."

	// stackOverflowQuestionTitle is 85a60cef's title.
	stackOverflowQuestionTitle = "Status bar and navigation bar appear over my view's bounds in iOS 7"
)
